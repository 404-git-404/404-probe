package updater

import "context"

func Serve(ctx context.Context) error { return platformServe(ctx) }
