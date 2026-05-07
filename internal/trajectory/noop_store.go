package trajectory

import "context"

type NoopStore struct{}

func (NoopStore) Save(context.Context, Snapshot) error {
	return nil
}

func (NoopStore) Get(context.Context, string, int) (Snapshot, error) {
	return Snapshot{}, ErrNotFound
}
