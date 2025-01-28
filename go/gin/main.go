package main

import (
	"github.com/gin-gonic/gin"
)

func main() {
	// 创建一个默认的 Gin 路由引擎
	r := gin.Default()

	// 定义一个 GET 请求的路由
	r.GET("/hello", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "Hello, World!",
		})
	})

	r.GET("/user/:name", func(c *gin.Context) {
		name := c.Param("name")
		c.JSON(200, gin.H{
			"user": name,
		})
	})

	r.GET("/search", func(c *gin.Context) {
		query := c.Query("q")
		c.JSON(200, gin.H{
			"query": query,
		})
	})

	// 启动 HTTP 服务器，监听端口 8080
	r.Run(":8080")
};