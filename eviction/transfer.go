package eviction

import (
	"path/filepath"
	"strings"
)

type transfer struct {
	listenDir string
	cacheDir  string
}

func newTransfer(listenDir, cacheDir string) *transfer {
	return &transfer{
		listenDir: listenDir,
		cacheDir:  cacheDir,
	}
}

func (t *transfer) tran(listenDir string) (string, error) {
	base, err := filepath.Rel(t.listenDir, listenDir)
	if err != nil {
		return "", err
	}
	pathList := strings.Split(base, "/")
	idx := 0
	for n := 0; n < len(pathList); n++ {
		if n == 1 {
			idx = n
			break
		}
	}
	pathList = pathList[idx:]
	return filepath.Join(t.cacheDir, filepath.Join(pathList...)), nil
}
