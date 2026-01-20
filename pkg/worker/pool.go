package worker

import (
	"sync"

	"github.com/wunzeco/isbn-resolver/pkg/resolver"
)

// Job represents a work item containing an ISBN to resolve
type Job struct {
	ISBN  string
	Index int
}

// Result represents the result of resolving an ISBN
type Result struct {
	Index    int
	Metadata *resolver.BookMetadata
	Error    error
}

// Pool manages a pool of workers to process ISBNs concurrently
type Pool struct {
	jobs    chan Job
	results chan Result
	wg      sync.WaitGroup
	client  *resolver.APIClient
}

// NewPool creates a new worker pool
func NewPool(numWorkers int, client *resolver.APIClient) *Pool {
	pool := &Pool{
		jobs:    make(chan Job, numWorkers*2),
		results: make(chan Result, numWorkers*2),
		client:  client,
	}

	// Start workers
	for i := 0; i < numWorkers; i++ {
		pool.wg.Add(1)
		go pool.worker()
	}

	return pool
}

// worker processes jobs from the jobs channel
func (p *Pool) worker() {
	defer p.wg.Done()

	for job := range p.jobs {
		metadata, err := p.client.Resolve(job.ISBN)
		if metadata != nil {
			metadata.ISBN = job.ISBN
		}

		p.results <- Result{
			Index:    job.Index,
			Metadata: metadata,
			Error:    err,
		}
	}
}

// Submit adds a job to the pool
func (p *Pool) Submit(job Job) {
	p.jobs <- job
}

// Results returns the results channel
func (p *Pool) Results() <-chan Result {
	return p.results
}

// Close closes the worker pool and waits for all workers to finish
func (p *Pool) Close() {
	close(p.jobs)
	p.wg.Wait()
	close(p.results)
}
