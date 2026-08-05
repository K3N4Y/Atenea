package tool

import (
	"fmt"
	"os"
)

type fakeEditFS struct {
	files  map[string][]byte
	writes map[string][]byte
}

func (f *fakeEditFS) ReadFile(name string) ([]byte, error) {
	b, ok := f.files[name]
	if !ok {
		return nil, fmt.Errorf("not found: %s", name)
	}
	return b, nil
}
func (f *fakeEditFS) WriteFile(name string, data []byte, _ os.FileMode) error {
	if f.writes == nil {
		f.writes = map[string][]byte{}
	}
	f.writes[name] = data
	f.files[name] = data
	return nil
}
