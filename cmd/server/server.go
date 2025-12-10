package main

import (
	"fmt"
	"net"
)

func main() {
	addr := ":9000" // 监听端口

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		panic(err)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	fmt.Println("UDP Echo server is running at", addr)

	buf := make([]byte, 2048)

	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			fmt.Println("Read error:", err)
			continue
		}

		// 打印收到的信息
		// fmt.Printf("recv from %s: %x\n", remote.String(), buf[:n])

		// echo 回去
		_, err = conn.WriteToUDP(buf[:n], remote)
		if err != nil {
			fmt.Println("Write error:", err)
		}
	}
}
