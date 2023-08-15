package eviction

import (
	"path/filepath"
	"strings"
)

type transfer struct {
	listenDir string
	cacheDir  string
	startidx  int
}

func newTransfer(listenDir, cacheDir string) *transfer {
	idx := len(strings.Split(listenDir, "/"))
	return &transfer{
		listenDir: listenDir,
		cacheDir:  cacheDir,
		startidx:  idx,
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
		if pathList[n] == "ac" || pathList[n] == "cas" || pathList[n] == "content_addressable" || n == t.startidx {
			idx = n
			break
		}
	}
	pathList = pathList[idx:]
	return filepath.Join(t.cacheDir, filepath.Join(pathList...)), nil
}
