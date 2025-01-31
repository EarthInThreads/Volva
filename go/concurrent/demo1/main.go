package main

import (
	"fmt"
	"time"
)

// 发送数据的函数
func sender(ch chan string) {
    for i := 1; i <= 5; i++ {
        msg := fmt.Sprintf("消息 #%d", i)
        ch <- msg // 发送消息到channel
        time.Sleep(time.Millisecond * 500) // 暂停0.5秒
    }
    close(ch) // 发送完成后关闭channel
}

// 接收数据的函数
func receiver(ch chan string, done chan bool) {
    for msg := range ch { // 持续从channel接收数据，直到channel关闭
        fmt.Printf("收到: %s\n", msg)
    }
    done <- true // 通知主程序接收完成
}

func main() {
    ch := make(chan string)    // 创建用于传递消息的channel
    done := make(chan bool)    // 创建用于同步的channel

    // 启动sender和receiver #goroutine#
    go sender(ch)
    go receiver(ch, done)

    // 等待receiver完成
    <-done
    fmt.Println("程序执行完成")
}