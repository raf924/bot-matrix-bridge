package utils

import "fmt"

type PipeLine struct {
	err error
}

func (p *PipeLine) Then(f func() error, errorMessage ...string) *PipeLine {
	if p.err == nil {
		p.err = f()
		if p.err != nil && len(errorMessage) > 0 {
			p.err = fmt.Errorf("%s: %w", errorMessage[0], p.err)
		}
	}
	return p
}

func (p *PipeLine) Err(errorMessage ...string) error {
	if p.err != nil && len(errorMessage) > 0 {
		p.err = fmt.Errorf("%s: %w", errorMessage[0], p.err)
	}
	return p.err
}
