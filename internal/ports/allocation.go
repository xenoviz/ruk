package ports

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/xenoviz/ruk/internal/state"
)

// ReservationSession is the transaction surface needed by assignment port
// allocation. Registry exposes this interface without leaking its concrete
// transaction into lifecycle composition or tests.
type ReservationSession interface {
	Reserved() map[int64]struct{}
	Reserve(port int64, assignmentID, statePath string) error
	Commit() error
}

// ReservationRegistry serializes a host-wide reservation transaction.
type ReservationRegistry interface {
	WithReservations(context.Context, func(ReservationSession) error) error
}

// AssignmentStore is the durable state mutation boundary used by allocation.
type AssignmentStore interface {
	Update(context.Context, func(*state.State) error) error
}

// PortFinder probes one host-local port outside the supplied exclusion set.
type PortFinder interface {
	Find(map[int64]struct{}) (int64, error)
}

// AllocationService reserves host ports before publishing them into an
// assignment. Its lock order is host registry then repository state, matching
// the TypeScript implementation and preventing two repositories from claiming
// one host-local port.
type AllocationService struct {
	Store     AssignmentStore
	Registry  ReservationRegistry
	Finder    PortFinder
	StatePath string
}

// Allocate replaces the assignment's named-port map after every reservation
// has been committed. Registry-first publication is intentional: if the later
// state write fails, the inactive reservation is pruned on the next registry
// transaction; the inverse order could publish an unreserved port as usable.
func (service AllocationService) Allocate(ctx context.Context, assignmentID string, names []string) (state.WorkspaceRecord, error) {
	if service.Store == nil || service.Registry == nil || service.Finder == nil {
		return state.WorkspaceRecord{}, errors.New("port allocation service is not configured")
	}
	if assignmentID == "" {
		return state.WorkspaceRecord{}, errors.New("assignment ID must not be empty")
	}
	if !filepath.IsAbs(service.StatePath) {
		return state.WorkspaceRecord{}, errors.New("port allocation state path must be absolute")
	}
	if err := validateAllocationNames(names); err != nil {
		return state.WorkspaceRecord{}, err
	}

	var result state.WorkspaceRecord
	err := service.Registry.WithReservations(ctx, func(reservations ReservationSession) error {
		if reservations == nil {
			return errors.New("port reservation transaction is unavailable")
		}
		return service.Store.Update(ctx, func(current *state.State) error {
			key, workspace, exists := allocationAssignment(current, assignmentID)
			if !exists {
				return fmt.Errorf("Assignment %s does not exist", assignmentID)
			}
			if workspace.Lifecycle != state.LifecycleAssigned || workspace.Assignment == nil {
				return fmt.Errorf("Workspace %s is %s, expected assigned", workspace.Path, workspace.Lifecycle)
			}

			excluded := reservations.Reserved()
			if excluded == nil {
				excluded = map[int64]struct{}{}
			}
			for _, managed := range current.Workspaces {
				if managed.Assignment == nil {
					continue
				}
				for _, port := range managed.Assignment.Ports {
					excluded[port] = struct{}{}
				}
			}

			allocated := make(map[string]int64, len(names))
			for _, name := range names {
				port, err := service.Finder.Find(excluded)
				if err != nil {
					return err
				}
				if ValidatePort(port) != nil {
					return fmt.Errorf("Port allocator returned unavailable port %d", port)
				}
				if _, occupied := excluded[port]; occupied {
					return fmt.Errorf("Port allocator returned unavailable port %d", port)
				}
				excluded[port] = struct{}{}
				allocated[name] = port
				if err := reservations.Reserve(port, assignmentID, service.StatePath); err != nil {
					return err
				}
			}
			if err := reservations.Commit(); err != nil {
				return err
			}
			workspace.Assignment.Ports = allocated
			current.Workspaces[key] = workspace
			result = workspace
			return nil
		})
	})
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	return result, nil
}

func validateAllocationNames(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		environment, err := NormalizeName(name)
		if err != nil {
			return err
		}
		if _, exists := seen[environment]; exists {
			return errors.New("Port names must be unique after normalization")
		}
		seen[environment] = struct{}{}
	}
	return nil
}

func allocationAssignment(current *state.State, assignmentID string) (string, state.WorkspaceRecord, bool) {
	for key, workspace := range current.Workspaces {
		if workspace.Assignment != nil && workspace.Assignment.ID == assignmentID {
			return key, workspace, true
		}
	}
	return "", state.WorkspaceRecord{}, false
}
