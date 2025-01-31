package main

import (
	"fmt"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// User 定义用户模型
type User struct {
    ID      uint   `gorm:"primaryKey"`
    Name    string `gorm:"size:255;not null"`
    Email   string `gorm:"size:255;uniqueIndex"`
    Age     int
    Address string
}

func main() {
    // 配置MySQL连接
    dsn := "root:123456@tcp(127.0.0.1:3306)/test_db?charset=utf8mb4&parseTime=True&loc=Local"
    db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
    if err != nil {
        panic("failed to connect database")
    }

    // 自动迁移 schema
    db.AutoMigrate(&User{})

    // 创建用户
    user := User{
        Name:    "张三",
        Email:   "zhangsan@example.com",
        Age:     25,
        Address: "北京市",
    }
    result := db.Create(&user)
    if result.Error != nil {
        fmt.Printf("创建用户失败: %v\n", result.Error)
    }
    fmt.Printf("创建的用户ID: %d\n", user.ID)

    // 查询用户
    var findUser User
    db.First(&findUser, user.ID) // 通过ID查询
    fmt.Printf("查询到的用户: %+v\n", findUser)

    // 更新用户
    db.Model(&findUser).Updates(User{
        Age:     26,
        Address: "上海市",
    })

    // 查询所有用户
    var users []User
    db.Find(&users)
    fmt.Printf("所有用户: %+v\n", users)

    // 删除用户
    // db.Delete(&user)
}