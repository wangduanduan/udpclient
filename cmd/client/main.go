package main

import (
	"context"
	"time"

	"udpclient"

	"go.uber.org/zap"
)

func main() {

	logger, _ := zap.NewProduction()
	defer logger.Sync() // flushes buffer, if any
	log := logger.Sugar()
	// log.Infow("failed to fetch URL",
	// 	// Structured context as loosely typed key-value pairs.
	// 	"attempt", 3,
	// 	"backoff", time.Second,
	// )
	// log.Infof("Failed to fetch URL: %s", "example.com")

	// 创建客户端
	client, err := udpclient.New(udpclient.Config{
		RemoteAddr:    "127.0.0.1:9000",
		SugaredLogger: log,
	})

	if err != nil {
		log.Fatalf("Failed to create UDP client: %v", err)
	}
	defer client.Close()

	// 同步发送示例
	data := []byte("Hello, UDP Server!")

	// 方式3: 带上下文的发送
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := client.Request(ctx, data)
	if err != nil {
		log.Infof("Send with context error: %v", err)
	} else {
		log.Infof("Received: %s", string(resp))
	}
}
