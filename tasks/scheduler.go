package tasks

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/tendermint/tendermint/abci/types"
	"golang.org/x/sync/errgroup"
)

type status string

const (
	statusPending   status = "pending"
	statusExecuted  status = "executed"
	statusAborted   status = "aborted"
	statusValidated status = "validated"
)

type deliverTxTask struct {
	Status      status
	Index       int
	Incarnation int
	Request     types.RequestDeliverTx
	Response    *types.ResponseDeliverTx
}

// Scheduler processes tasks concurrently
type Scheduler interface {
	ProcessAll(ctx sdk.Context, reqs []types.RequestDeliverTx) ([]types.ResponseDeliverTx, error)
}

type scheduler struct {
	deliverTx func(ctx sdk.Context, req types.RequestDeliverTx) (res types.ResponseDeliverTx)
	workers   int
}

// NewScheduler creates a new scheduler
func NewScheduler(workers int, deliverTxFunc func(ctx sdk.Context, req types.RequestDeliverTx) (res types.ResponseDeliverTx)) Scheduler {
	return &scheduler{
		workers:   workers,
		deliverTx: deliverTxFunc,
	}
}

func toTasks(reqs []types.RequestDeliverTx) []*deliverTxTask {
	res := make([]*deliverTxTask, 0, len(reqs))
	for idx, r := range reqs {
		res = append(res, &deliverTxTask{
			Request: r,
			Index:   idx,
			Status:  statusPending,
		})
	}
	return res
}

func collectResponses(tasks []*deliverTxTask) []types.ResponseDeliverTx {
	res := make([]types.ResponseDeliverTx, 0, len(tasks))
	for _, t := range tasks {
		res = append(res, *t.Response)
	}
	return res
}

func (s *scheduler) ProcessAll(ctx sdk.Context, reqs []types.RequestDeliverTx) ([]types.ResponseDeliverTx, error) {
	tasks := toTasks(reqs)
	toExecute := tasks
	for len(toExecute) > 0 {

		// execute sets statuses of tasks to either executed or aborted
		err := s.executeAll(ctx, toExecute)
		if err != nil {
			return nil, err
		}

		// validate returns any that should be re-executed
		// note this processes ALL tasks, not just those recently executed
		toExecute, err = s.validateAll(ctx, tasks)
		if err != nil {
			return nil, err
		}
		for _, t := range toExecute {
			t.Incarnation++
			t.Status = statusPending
			//TODO: reset anything that needs resetting
		}
	}
	return collectResponses(tasks), nil
}

// TODO: validate each tasks
// TODO: return list of tasks that are invalid
func (s *scheduler) validateAll(ctx sdk.Context, tasks []*deliverTxTask) ([]*deliverTxTask, error) {
	var res []*deliverTxTask
	for _, t := range tasks {
		// any aborted tx is known to be suspect here
		if t.Status == statusAborted {
			res = append(res, t)
		} else {
			//TODO: validate the tasks and add it if invalid
			//TODO: create and handle abort for validation
			t.Status = statusValidated
		}
	}
	return res, nil
}

// ExecuteAll executes all tasks concurrently
// Tasks are updated with their status
// TODO: retries on aborted tasks
// TODO: error scenarios
func (s *scheduler) executeAll(ctx sdk.Context, tasks []*deliverTxTask) error {
	ch := make(chan *deliverTxTask, len(tasks))
	grp, gCtx := errgroup.WithContext(ctx.Context())

	// a workers value < 1 means no limit
	workers := s.workers
	if s.workers < 1 {
		workers = len(tasks)
	}

	for i := 0; i < workers; i++ {
		grp.Go(func() error {
			for {
				select {
				case <-gCtx.Done():
					return gCtx.Err()
				case task, ok := <-ch:
					if !ok {
						return nil
					}
					//TODO: ensure version multi store is on context
					//abortCh := make(chan Abort)

					//TODO: consume from abort in non-blocking way (give it a length)
					resp := s.deliverTx(ctx, task.Request)

					//if _, ok := <-abortCh; ok {
					//	tasks.status = TaskStatusAborted
					//	continue
					//}

					task.Status = statusExecuted
					task.Response = &resp
				}
			}
		})
	}
	grp.Go(func() error {
		defer close(ch)
		for _, task := range tasks {
			select {
			case <-gCtx.Done():
				return gCtx.Err()
			case ch <- task:
			}
		}
		return nil
	})

	if err := grp.Wait(); err != nil {
		return err
	}

	return nil
}