package main

import (
	pb "backEnd/proto/helloworld"
	"context"
	"log"
	"net/http"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	conn, err := grpc.DialContext(context.Background(), "localhost:50051", 
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	// 创建gRPC客户端
	client := pb.NewGreeterClient(conn)

	// 创建Gin路由引擎
	r := gin.Default()

	// 处理CORS
	r.Use(cors.Default())

	// 注册路由处理函数
	r.GET("/hello/:name", func(c *gin.Context) {
		// 从URL参数中获取name
		name := c.Param("name")

		// 调用Python gRPC服务
		resp, err := client.SayHello(context.Background(), &pb.HelloRequest{Name: name})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": err.Error(),
			})
			return
		}

		// 返回响应
		c.JSON(http.StatusOK, gin.H{
			"message": resp.Message,
		})
	})

	// 启动服务器
	r.Run(":8080")
}
