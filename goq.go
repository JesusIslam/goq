package goq

import (
	"encoding/base32"
	"encoding/json"
	"errors"
	"gopkg.in/redis.v3"
	"time"
)

const (
	JOB_STATUS_PREFIX = "goq:queue:job:status:"
)

var (
	client *redis.Client
)

type Processor func(*Job)

type ErrorHandler func(error)

type ConnectionOptions struct {
	Addr string
	Password string
	DB int64
	MaxRetries int
	DialTimeout time.Duration
	ReadTimeout time.Duration
	WriteTimeout time.Duration
	PoolSize int
	PoolTimeout time.Duration
	IdleTimeout time.Duration
}

type Options struct {
	Connection   *ConnectionOptions
	Concurrency  uint8
	QueueName    string
	Processor    Processor
	ErrorHandler ErrorHandler
}

func New(opt *Options) *Queue {
	if client == nil {
		redisOpt := &redis.Options{
			Addr: opt.Connection.Addr,
			Password: opt.Connection.Password,
			DB: opt.Connection.DB,
			MaxRetries: opt.Connection.MaxRetries,
			DialTimeout: opt.Connection.DialTimeout,
			ReadTimeout: opt.Connection.ReadTimeout,
			WriteTimeout: opt.Connection.WriteTimeout,
			PoolSize: opt.Connection.PoolSize,
			PoolTimeout: opt.Connection.PoolTimeout,
			IdleTimeout: opt.Connection.IdleTimeout,
		}
		client = redis.NewClient(redisOpt)
	}

	return &Queue{
		jobChannel:   make(chan string, 1000),
		concurrency:  opt.Concurrency,
		queueName:    opt.QueueName,
		processor:    opt.Processor,
		errorHandler: opt.ErrorHandler,
	}
}

type Queue struct {
	jobChannel   chan string
	concurrency  uint8
	queueName    string
	processor    Processor
	errorHandler ErrorHandler
}

type QueueStatus struct {
	QueueLength int64
}

func (q *Queue) QueueStatus() (*QueueStatus, error) {
	if client != nil {
		queueLen, err := client.LLen(q.queueName).Result()
		if err != nil {
			return nil, err
		}

		return &QueueStatus{
			QueueLength: queueLen,
		}, nil
	}

	return nil, errors.New("Failed to queue status: no initialized client")
}

// Method to enqueue job to queue, returns job id
func (q *Queue) Enqueue(jobJSON string) (string, error) {
	var err error
	// push to queue
	err = client.RPush(q.queueName, jobJSON).Err()
	if err != nil {
		return "", err
	}

	// create status JSON
	statusJSON, err := json.Marshal(&Status{
		Code:     0,
		Progress: 0,
	})
	if err != nil {
		return "", err
	}
	// create id
	id := base32.StdEncoding.EncodeToString([]byte(jobJSON))
	// set status of this job
	err = client.Set(JOB_STATUS_PREFIX+id, string(statusJSON), 0).Err()
	if err != nil {
		return "", err
	}

	return id, nil
}

func (q *Queue) Run() {
	for i := uint8(0); i < q.concurrency; i++ {
		go work(q.jobChannel, q.errorHandler, q.processor)
	}
	for {
		// dequeue the job
		// jobJSONSlice will always be 2 length
		jobJSONSlice, err := client.BLPop(0, q.queueName).Result()
		if err != nil {
			q.errorHandler(err)
			continue
		}

		q.jobChannel <- jobJSONSlice[1]
	}
}

func work(jobChannel <-chan string, errorHandler ErrorHandler, processor Processor) {
	for {
		jobJSON := <-jobChannel
		// create the id
		id := base32.StdEncoding.EncodeToString([]byte(jobJSON))
		// check status
		statusJSON, err := client.Get(JOB_STATUS_PREFIX + id).Result()
		if err != nil {
			errorHandler(errors.New("Failed to get status of job " + id + " : " + err.Error()))
			continue
		}
		// unmarshal the status
		status := &Status{}
		err = json.Unmarshal([]byte(statusJSON), status)
		if err != nil {
			errorHandler(errors.New("Failed to unmarshal status of job " + id + " : " + err.Error()))
			continue
		}
		// create a job
		job := &Job{
			ID:     id,
			JSON:   jobJSON,
			Status: status,
		}
		// process it
		processor(job)
	}
}

type Job struct {
	ID     string
	JSON   string
	Status *Status
}

func (j *Job) SetStatus(code, progress uint8) error {
	j.Status.Code = code
	j.Status.Progress = progress

	statusJSON, err := json.Marshal(j.Status)
	if err != nil {
		return err
	}

	return client.Set(JOB_STATUS_PREFIX+j.ID, string(statusJSON), 0).Err()
}

func (j *Job) GetStatus() error {
	dataJSON, err := client.Get(JOB_STATUS_PREFIX + j.ID).Result()
	if err != nil {
		return err
	}

	return json.Unmarshal([]byte(dataJSON), j.Status)
}

type Status struct {
	Code     uint8
	Progress uint8
}
