package udpclient

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"

	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

type pendingEntry struct {
	ch chan []byte // buffered chan (len=1)
}

type Client struct {
	conn       *net.UDPConn
	remoteAddr *net.UDPAddr
	shards     []sync.Map // 每个 shard 存储 map[uint32]*pendingEntry
	shardMask  uint32
	closed     int32

	Config
}

type Config struct {
	MaxAttempts int    // 请求失败时最大重试次数，可选，（包括第一次），默认 1
	MinRetryMs  int    // 重试的最小退避时间，可选，单位毫秒，默认 50ms
	ShardBits   uint   // 分片数量的位数，可选，默认 8 (256 shards), 必须是 2 的幂
	LocalAddr   string // 本地地址, 可选，默认 "0.0.0.0:0"
	RemoteAddr  string // 远程地址, 必须

	*zap.SugaredLogger
}

func isPowerOfTwo(n int) bool {
	return n > 0 && (n&(n-1)) == 0
}

func New(c Config) (*Client, error) {
	if c.ShardBits == 0 || c.ShardBits > 16 {
		c.ShardBits = 8 // 默认 256 shards
	}

	if c.MaxAttempts < 1 || c.MaxAttempts > 10 {
		c.MaxAttempts = 10
	}

	if c.MinRetryMs < 50 {
		c.MinRetryMs = 50
	}

	if c.LocalAddr == "" {
		c.LocalAddr = "0.0.0.0:0"
	}

	if c.RemoteAddr == "" {
		return nil, errors.New("remoteAddr is required")
	}

	if !isPowerOfTwo(int(c.ShardBits)) {
		return nil, fmt.Errorf("shardBits must be power of two: %d", c.ShardBits)
	}

	laddr, err := net.ResolveUDPAddr("udp", c.LocalAddr)
	if err != nil {
		return nil, err
	}

	raddr, err := net.ResolveUDPAddr("udp", c.RemoteAddr)
	if err != nil {
		return nil, err
	}

	conn, err := net.DialUDP("udp", laddr, raddr)
	if err != nil {
		return nil, err
	}

	sz := uint32(1 << c.ShardBits)

	client := &Client{
		conn:       conn,
		remoteAddr: raddr,
		shards:     make([]sync.Map, sz),
		shardMask:  sz - 1,
		Config:     c,
	}

	go client.readLoop()
	return client, nil
}

func (c *Client) RetryBackoff(attempt int) time.Duration {
	// 指数退避，基础 50ms
	base := time.Duration(c.MinRetryMs) * time.Millisecond
	// limit a reasonable bound
	exp := time.Duration(math.Pow(2, float64(attempt-1)))
	wait := time.Duration(exp) * base
	if wait > 2*time.Second {
		wait = 2 * time.Second
	}
	return wait
}

func (c *Client) Close() error {
	if !atomic.CompareAndSwapInt32(&c.closed, 0, 1) {
		return nil
	}

	c.conn.Close()

	return nil
}

// Request 发送 data，并等待响应或 ctx 取消。
// 返回第一个收到的响应（如果有重试，仍然只返回第一个到达并匹配 reqID 的响应）。
// 语义：如果 ctx.Done()，会立即返回 ctx.Err()（并移除 pending）。
func (c *Client) Request(ctx context.Context, data []byte) ([]byte, error) {
	if atomic.LoadInt32(&c.closed) == 1 {
		return nil, errors.New("client closed")
	}

	reqID := c.nextReqID()

	entry := &pendingEntry{ch: make(chan []byte, 1)}
	c.storePending(reqID, entry)
	defer c.deletePending(reqID)

	// 构造包：4 bytes reqID (big-endian) + payload
	pkt := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(pkt[:4], reqID)
	copy(pkt[4:], data)

	// 尝试发送并等待响应，支持重试
	var lastErr error
	attempts := c.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		// 优先检查 ctx
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// 发送
		_, err := c.conn.Write(pkt)
		if err != nil {
			lastErr = err
			// 如果还有机会重试且 ctx 未过期，等待 backoff
			if attempt < attempts {
				select {
				case <-time.After(c.RetryBackoff(attempt)):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}
			return nil, err
		}

		// 等待响应或 ctx 取消
		select {
		case resp := <-entry.ch:
			return resp, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.retryOrDeadlineWait(ctx, attempt)):
			// 超时（按退避或 deadline 触发）
			lastErr = errors.New("request timeout / no response")
			// 继续重试（如果有）
			if attempt < attempts {
				// wait backoff before next attempt
				select {
				case <-time.After(c.RetryBackoff(attempt)):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}
			return nil, lastErr
		}
	}

	return nil, lastErr
}

// retryOrDeadlineWait: 决定在单次发送后等待多久来接收响应：
// - 如果 ctx 有 deadline，我们等待到 deadline
// - 否则使用默认的退避上限（例如 1s * attempt）
func (c *Client) retryOrDeadlineWait(ctx context.Context, attempt int) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		now := time.Now()
		if dl.After(now) {
			return dl.Sub(now)
		}
		return 0
	}
	// 没有 deadline，使用一个合理的单次等待上限
	// e.g., base 500ms * attempt, 上限 2s
	base := 500 * time.Millisecond
	wait := time.Duration(attempt) * base
	if wait > 2*time.Second {
		wait = 2 * time.Second
	}
	return wait
}

func (c *Client) readLoop() {
	buf := make([]byte, 2048)

	for {
		if atomic.LoadInt32(&c.closed) == 1 {
			return
		}

		n, err := c.conn.Read(buf)
		if err != nil {
			return
		}
		if n < 4 {
			continue
		}
		reqID := binary.BigEndian.Uint32(buf[:4])
		payload := make([]byte, n-4)
		copy(payload, buf[4:n])

		val, ok := c.loadPending(reqID)
		if !ok {
			// 未找到对应 pending，丢弃
			continue
		}
		// 非阻塞发送到 chan，防止读协程被阻塞（请求方可能已超时返回）
		select {
		case val.ch <- payload:
		default:
		}
	}
}

// 存取 pending 状态的channel
func (c *Client) storePending(reqID uint32, e *pendingEntry) {
	sh := c.shardFor(reqID)
	sh.Store(reqID, e)
}
func (c *Client) loadPending(reqID uint32) (*pendingEntry, bool) {
	sh := c.shardFor(reqID)
	val, ok := sh.Load(reqID)
	if !ok {
		return nil, false
	}
	return val.(*pendingEntry), true
}
func (c *Client) deletePending(reqID uint32) {
	sh := c.shardFor(reqID)
	sh.Delete(reqID)
}

func (c *Client) shardFor(reqID uint32) *sync.Map {
	idx := reqID & c.shardMask
	c.Infow("shard", "reqID", reqID, "idx", idx)
	return &c.shards[idx]
}

var globalReqID uint32

func (c *Client) nextReqID() uint32 {
	for {
		id := atomic.AddUint32(&globalReqID, 1)
		if id == 0 {
			continue
		}

		shard := c.shardFor(id)
		if _, exists := shard.Load(id); !exists {
			return id
		}
	}
}
