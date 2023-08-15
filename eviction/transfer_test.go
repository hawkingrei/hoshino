package eviction

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTransfer(t *testing.T) {
	tran := newTransfer("/mnt/kubernetes-disks-bazel/", "/data1/bazel/cache")
	actual, err := tran.tran("/mnt/kubernetes-disks-bazel/disk3/2c389379-351c-4b6d-a402-ad03b7b7d449")
	require.NoError(t, err)
	require.Equal(t, actual, "/data1/bazel/cache/2c389379-351c-4b6d-a402-ad03b7b7d449")
}
