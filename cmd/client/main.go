package main

import (
	"context"
	"log"
	"time"

	"udpclient"
)

func main() {
	// 创建客户端
	client, err := udpclient.NewClientDial("0.0.0.0:0", "127.0.0.1:8080", 8)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	// 同步发送示例
	data := []byte("Hello, UDP Server!")

	// 方式3: 带上下文的发送
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := client.Request(ctx, data)
	if err != nil {
		log.Printf("Send with context error: %v", err)
	} else {
		log.Printf("Received: %s", string(resp[4:]))
	}
}
