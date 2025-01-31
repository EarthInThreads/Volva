package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// 错误定义
var (
    ErrTaskTimeout    = errors.New("task execution timeout")
    ErrPoolOverload   = errors.New("worker pool overloaded")
    ErrTaskValidation = errors.New("task validation failed")
)

// 任务优先级
type Priority int

const (
    LowPriority Priority = iota
    MediumPriority
    HighPriority
)

// 任务状态
type TaskStatus int32

const (
    TaskPending TaskStatus = iota
    TaskRunning
    TaskCompleted
    TaskFailed
)

// 任务接口
type Task interface {
    Execute(ctx context.Context) (interface{}, error)
    GetID() string
    GetPriority() Priority
}

// 基础任务结构
type BaseTask struct {
    ID       string
    Priority Priority
    Status   TaskStatus
    Retries  int32
    Data     interface{}
    Result   interface{}
    Error    error
    Created  time.Time
}

// 任务结果
type TaskResult struct {
    TaskID    string
    Result    interface{}
    Error     error
    StartTime time.Time
    EndTime   time.Time
}

// 工作池配置
type WorkerPoolConfig struct {
    NumWorkers     int
    QueueSize      int
    RateLimit      float64
    MaxRetries     int32
    TaskTimeout    time.Duration
    RetryInterval  time.Duration
    MetricsEnabled bool
}

// 工作池结构
type WorkerPool struct {
    config       WorkerPoolConfig
    tasks        chan Task
    results      chan TaskResult
    metrics      *Metrics
    limiter      *rate.Limiter
    ctx          context.Context
    cancel       context.CancelFunc
    wg           sync.WaitGroup
    errorHandler func(error)
    mu           sync.RWMutex
    workers      map[int]*Worker
}

// 指标收集器
type Metrics struct {
    TasksProcessed   uint64
    TasksFailed      uint64
    TasksSucceeded   uint64
    ProcessingTime   time.Duration
    mu              sync.RWMutex
    taskLatencies   []time.Duration
}

// 工作者结构
type Worker struct {
    ID        int
    pool      *WorkerPool
    taskCount uint64
    // lastError error
}

// 创建新的工作池
func NewWorkerPool(config WorkerPoolConfig) *WorkerPool {
    ctx, cancel := context.WithCancel(context.Background())
    
    wp := &WorkerPool{
        config:       config,
        tasks:        make(chan Task, config.QueueSize),
        results:      make(chan TaskResult, config.QueueSize),
        metrics:      &Metrics{taskLatencies: make([]time.Duration, 0)},
        limiter:      rate.NewLimiter(rate.Limit(config.RateLimit), 1),
        ctx:          ctx,
        cancel:       cancel,
        workers:      make(map[int]*Worker),
        errorHandler: defaultErrorHandler,
    }

    return wp
}

// 启动工作池
func (wp *WorkerPool) Start() {
    // 启动工作者
    for i := 0; i < wp.config.NumWorkers; i++ {
        worker := &Worker{
            ID:   i + 1,
            pool: wp,
        }
        wp.mu.Lock()
        wp.workers[i+1] = worker
        wp.mu.Unlock()
        
        wp.wg.Add(1)
        go worker.start()
    }

    // 启动指标收集
    if wp.config.MetricsEnabled {
        go wp.collectMetrics()
    }

    // 启动信号处理
    go wp.handleSignals()
}

// 工作者处理逻辑
func (w *Worker) start() {
    defer w.pool.wg.Done()

    for {
        select {
        case <-w.pool.ctx.Done():
            return
        case task, ok := <-w.pool.tasks:
            if !ok {
                return
            }

            // 速率限制
            err := w.pool.limiter.Wait(w.pool.ctx)
            if err != nil {
                continue
            }

            // 执行任务
            result := w.processTask(task)
            
            // 更新指标
            atomic.AddUint64(&w.taskCount, 1)
            if result.Error != nil {
                atomic.AddUint64(&w.pool.metrics.TasksFailed, 1)
            } else {
                atomic.AddUint64(&w.pool.metrics.TasksSucceeded, 1)
            }

            // 发送结果
            w.pool.results <- result
        }
    }
}

// 任务处理
func (w *Worker) processTask(task Task) TaskResult {
    log.Printf("Worker %d starting task %s", w.ID, task.GetID())
    startTime := time.Now()
    result := TaskResult{
        TaskID:    task.GetID(),
        StartTime: startTime,
    }

    // 创建带超时的上下文
    ctx, cancel := context.WithTimeout(w.pool.ctx, w.pool.config.TaskTimeout)
    defer cancel()

    // 执行任务
    done := make(chan struct{})
    go func() {
        defer close(done)
        taskResult, err := task.Execute(ctx)
        result.Result = taskResult
        result.Error = err
    }()

    // 等待任务完成或超时
    select {
    case <-ctx.Done():
        result.Error = ErrTaskTimeout
    case <-done:
    }

    result.EndTime = time.Now()
    log.Printf("Worker %d completed task %s", w.ID, task.GetID())
    return result
}

// 提交任务
func (wp *WorkerPool) SubmitTask(task Task) error {
    if wp.ctx.Err() != nil {
        return errors.New("worker pool is stopped")
    }

    select {
    case wp.tasks <- task:
        log.Printf("Successfully submitted task: %s", task.GetID())
        return nil
    default:
        return ErrPoolOverload
    }
}

// 优雅关闭
func (wp *WorkerPool) Shutdown(timeout time.Duration) error {
    // 发送取消信号
    wp.cancel()

    // 创建关闭通道
    done := make(chan struct{})
    go func() {
        wp.wg.Wait()
        close(done)
    }()

    // 等待超时或完成
    select {
    case <-time.After(timeout):
        return errors.New("shutdown timeout")
    case <-done:
        return nil
    }
}

// 指标收集
func (wp *WorkerPool) collectMetrics() {
    ticker := time.NewTicker(time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-wp.ctx.Done():
            return
        case <-ticker.C:
            wp.metrics.mu.Lock()
            total := atomic.LoadUint64(&wp.metrics.TasksProcessed)
            failed := atomic.LoadUint64(&wp.metrics.TasksFailed)
            succeeded := atomic.LoadUint64(&wp.metrics.TasksSucceeded)
            
            // 计算平均延迟
            var avgLatency time.Duration
            if len(wp.metrics.taskLatencies) > 0 {
                var sum time.Duration
                for _, lat := range wp.metrics.taskLatencies {
                    sum += lat
                }
                avgLatency = sum / time.Duration(len(wp.metrics.taskLatencies))
            }
            
            log.Printf("Metrics - Total: %d, Succeeded: %d, Failed: %d, Avg Latency: %v",
                total, succeeded, failed, avgLatency)
            
            wp.metrics.mu.Unlock()
        }
    }
}

// 信号处理
func (wp *WorkerPool) handleSignals() {
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt)

    <-sigChan
    log.Println("Received shutdown signal, initiating graceful shutdown...")
    wp.Shutdown(30 * time.Second)
}

// 默认错误处理
func defaultErrorHandler(err error) {
    log.Printf("Error: %v", err)
}

// 示例任务实现
type ExampleTask struct {
    BaseTask
    ProcessingFunc func(context.Context) (interface{}, error)
}

func (t *ExampleTask) Execute(ctx context.Context) (interface{}, error) {
    return t.ProcessingFunc(ctx)
}

func (t *ExampleTask) GetID() string {
    return t.ID
}

func (t *ExampleTask) GetPriority() Priority {
    return t.Priority
}

// 主函数
func main() {
    // 配置工作池
    config := WorkerPoolConfig{
        NumWorkers:     5,
        QueueSize:      100,
        RateLimit:      10.0, // 每秒处理10个任务
        MaxRetries:     3,
        TaskTimeout:    5 * time.Second,
        RetryInterval:  time.Second,
        MetricsEnabled: true,
    }

    // 创建并启动工作池
    pool := NewWorkerPool(config)
    pool.Start()

    // 创建示例任务
    for i := 0; i < 20; i++ {
        task := &ExampleTask{
            BaseTask: BaseTask{
                ID:       fmt.Sprintf("task-%d", i),
                Priority: Priority(i % 3),
                Created:  time.Now(),
            },
            ProcessingFunc: func(ctx context.Context) (interface{}, error) {
                // 模拟任务处理
                time.Sleep(time.Duration(500+i*100) * time.Millisecond)
                return fmt.Sprintf("Result of task-%d", i), nil
            },
        }

        if err := pool.SubmitTask(task); err != nil {
            log.Printf("Failed to submit task: %v", err)
        } else {
            log.Printf("Successfully submitted task: %s", task.GetID())
        }
    }

    // 等待信号处理程序处理关闭
    select {}
}