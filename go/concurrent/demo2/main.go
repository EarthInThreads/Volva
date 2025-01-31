package main

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// 任务结构体
type Task struct {
    ID     int
    Data   string
    Result string
}

// 工作池结构体
type WorkerPool struct {
    numWorkers  int
    taskQueue   chan *Task
    resultQueue chan *Task
    wg          sync.WaitGroup
}

// 创建新的工作池
func NewWorkerPool(numWorkers int, queueSize int) *WorkerPool {
    return &WorkerPool{
        numWorkers:  numWorkers,
        taskQueue:   make(chan *Task, queueSize),
        resultQueue: make(chan *Task, queueSize),
    }
}

// 启动工作池
func (wp *WorkerPool) Start() {
    // 启动指定数量的工作协程
    for i := 1; i <= wp.numWorkers; i++ {
        wp.wg.Add(1)
        go wp.worker(i)
    }

    // 启动结果收集器
    go wp.resultCollector()
}

// 工作协程
func (wp *WorkerPool) worker(workerID int) {
    defer wp.wg.Done()

    for task := range wp.taskQueue {
        // 模拟处理任务
        fmt.Printf("工作者 #%d 开始处理任务 #%d\n", workerID, task.ID)
        time.Sleep(time.Duration(rand.Intn(1000)) * time.Millisecond)
        
        // 处理任务
        task.Result = fmt.Sprintf("任务 #%d 已被工作者 #%d 处理完成", task.ID, workerID)
        
        // 将结果发送到结果队列
        wp.resultQueue <- task
    }
}

// 结果收集器
func (wp *WorkerPool) resultCollector() {
    for result := range wp.resultQueue {
        fmt.Printf("收集到结果: %s\n", result.Result)
    }
}

// 提交任务
func (wp *WorkerPool) SubmitTask(task *Task) {
    wp.taskQueue <- task
}

// 关闭工作池
func (wp *WorkerPool) Stop() {
    close(wp.taskQueue)
    wp.wg.Wait()
    close(wp.resultQueue)
}

// 任务生成器
func taskGenerator(count int) []*Task {
    tasks := make([]*Task, count)
    for i := 0; i < count; i++ {
        tasks[i] = &Task{
            ID:   i + 1,
            Data: fmt.Sprintf("任务数据 #%d", i+1),
        }
    }
    return tasks
}

func main() {
    // 设置随机数种子
    rand.Seed(time.Now().UnixNano())

    // 创建工作池，设置3个工作者和10个缓冲区大小
    workerPool := NewWorkerPool(3, 10)
    
    // 启动工作池
    workerPool.Start()

    // 生成任务
    tasks := taskGenerator(10)

    // 提交任务
    fmt.Println("开始提交任务...")
    for _, task := range tasks {
        workerPool.SubmitTask(task)
    }

    // 等待一段时间确保任务处理完成
    fmt.Println("等待任务处理完成...")
    time.Sleep(time.Second * 3)

    // 停止工作池
    workerPool.Stop()
    fmt.Println("工作池已关闭")
}