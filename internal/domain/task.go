package domain

// TaskStatus represents the lifecycle state of a task (Spec 8.7 & 9).
type TaskStatus string

const (
	TaskTodo         TaskStatus = "todo"
	TaskInProgress   TaskStatus = "in_progress"
	TaskBlocked      TaskStatus = "blocked"
	TaskWaitingOwner TaskStatus = "waiting_owner"
	TaskInReview     TaskStatus = "in_review"
	TaskDone         TaskStatus = "done"
	TaskCancelled    TaskStatus = "cancelled"
)

// BlockedReason explains why a task is in TaskBlocked status.
type BlockedReason string

const (
	BlockedDependency BlockedReason = "dependency"
	BlockedStalled    BlockedReason = "stalled"
	BlockedError      BlockedReason = "error"
	BlockedNeedsOwner BlockedReason = "needs_owner"
)

// Final reports whether s is a terminal task status (done or cancelled).
func (s TaskStatus) Final() bool {
	return s == TaskDone || s == TaskCancelled
}

// CanTransition reports whether moving a task from -> to is valid per Spec 8.7.
func CanTransition(from, to TaskStatus) bool {
	if from == to || from.Final() {
		return false
	}
	switch from {
	case TaskTodo:
		switch to {
		case TaskInProgress, TaskBlocked, TaskWaitingOwner, TaskDone, TaskCancelled:
			return true
		}
	case TaskInProgress:
		switch to {
		case TaskTodo, TaskBlocked, TaskWaitingOwner, TaskInReview, TaskDone, TaskCancelled:
			return true
		}
	case TaskBlocked:
		switch to {
		case TaskTodo, TaskInProgress, TaskWaitingOwner, TaskCancelled:
			return true
		}
	case TaskWaitingOwner:
		switch to {
		case TaskTodo, TaskInProgress, TaskBlocked, TaskDone, TaskCancelled:
			return true
		}
	case TaskInReview:
		switch to {
		case TaskDone, TaskInProgress, TaskWaitingOwner, TaskBlocked, TaskCancelled:
			return true
		}
	}
	return false
}

// InitialStatus returns the starting status and blocked reason for a newly created task.
func InitialStatus(openBlockers int) (TaskStatus, *BlockedReason) {
	if openBlockers > 0 {
		r := BlockedDependency
		return TaskBlocked, &r
	}
	return TaskTodo, nil
}

// AutoUnblockable reports whether a blocked task may automatically return to todo when blockers complete.
func AutoUnblockable(r BlockedReason) bool {
	return r == BlockedDependency
}

// HasCycle reports whether adding the dependency edge "from is blocked by to" introduces a cycle in edges.
func HasCycle(edges map[ID][]ID, from, to ID) bool {
	if from == "" || to == "" {
		return false
	}
	if from == to {
		return true
	}
	visited := make(map[ID]bool, len(edges)+1)
	queue := []ID{to}
	visited[to] = true
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, next := range edges[curr] {
			if next == from {
				return true
			}
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
	}
	return false
}
