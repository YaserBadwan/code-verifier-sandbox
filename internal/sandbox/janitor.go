package sandbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/moby/moby/client"
)

// RemoveLeftovers deletes containers and volumes this service created that are still
// around, which only happens if a previous run crashed or was killed.
func (d *Docker) RemoveLeftovers(ctx context.Context) (containers, volumes int, err error) {
	filter := make(client.Filters).Add("label", LabelManaged+"=true")
	var errs []error

	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		return 0, 0, fmt.Errorf("listing leftover containers: %w", err)
	}
	for _, c := range list.Items {
		if _, err := d.cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			errs = append(errs, err)
			continue
		}
		containers++
	}

	vols, err := d.cli.VolumeList(ctx, client.VolumeListOptions{Filters: filter})
	if err != nil {
		return containers, 0, fmt.Errorf("listing leftover volumes: %w", err)
	}
	for _, v := range vols.Items {
		if err := d.RemoveVolume(ctx, v.Name); err != nil {
			errs = append(errs, err)
			continue
		}
		volumes++
	}
	return containers, volumes, errors.Join(errs...)
}
