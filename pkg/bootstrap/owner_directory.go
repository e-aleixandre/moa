package bootstrap

import (
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// ownerDirectory answers the tasks tool's questions about owners from
// owner.json, read on every call: a permission granted or revoked in Edit
// owner, or an owner created after this session started, counts at once.
type ownerDirectory struct{}

func (ownerDirectory) list() ([]owner.Owner, error) {
	store, err := owner.Default()
	if err != nil {
		return nil, err
	}
	return store.List()
}

func ownerRef(o owner.Owner) tasks.OwnerRef {
	return tasks.OwnerRef{ID: o.ID, Name: o.Name, SessionID: o.SessionID, ProjectKey: o.CodebaseKey, ProjectCWD: o.Root}
}

func (d ownerDirectory) OwnerOf(sessionID string) (tasks.OwnerRef, bool, error) {
	if sessionID == "" {
		return tasks.OwnerRef{}, false, nil
	}
	owners, err := d.list()
	if err != nil {
		return tasks.OwnerRef{}, false, err
	}
	for _, o := range owners {
		if o.SessionID == sessionID {
			return ownerRef(o), true, nil
		}
	}
	return tasks.OwnerRef{}, false, nil
}

func (d ownerDirectory) Find(nameOrID string) (tasks.OwnerRef, bool, error) {
	owners, err := d.list()
	if err != nil {
		return tasks.OwnerRef{}, false, err
	}
	var named []owner.Owner
	for _, o := range owners {
		if o.ID == nameOrID {
			return ownerRef(o), true, nil
		}
		if strings.EqualFold(strings.TrimSpace(o.Name), strings.TrimSpace(nameOrID)) {
			named = append(named, o)
		}
	}
	switch len(named) {
	case 0:
		return tasks.OwnerRef{}, false, nil
	case 1:
		return ownerRef(named[0]), true, nil
	}
	return tasks.OwnerRef{}, false, fmt.Errorf("%w: several owners are named %q; ask the user to rename one", tasks.ErrInvalid, nameOrID)
}

func (ownerDirectory) CanSend(fromID, toID string) (bool, error) {
	store, err := owner.Default()
	if err != nil {
		return false, err
	}
	from, found, err := store.FindByID(fromID)
	if err != nil || !found {
		return false, err
	}
	return from.MaySendTasksTo(toID), nil
}
