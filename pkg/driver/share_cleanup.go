package driver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// deleteShareByPath leaves storage intact if presentation cannot be queried or
// removed. Share types differ, but the retry safety policy must stay identical.
func deleteShareByPath[T any](ctx context.Context, protocol, path string,
	list func(context.Context) ([]T, error),
	identity func(T) (string, string),
	remove func(context.Context, string) error,
) error {

	shares, err := list(ctx)
	if err != nil {
		return status.Errorf(codes.Unavailable, "Cannot verify %s share cleanup: %v", protocol, err)
	}
	for _, share := range shares {
		id, sharePath := identity(share)
		if sharePath != path {
			continue
		}
		if err := remove(ctx, id); err != nil && !isNotFoundError(err) {
			return status.Errorf(codes.Unavailable, "Failed to delete %s share %s: %v", protocol, id, err)
		}
	}
	return nil
}
