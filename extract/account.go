package extract

import "fmt"

type accounting struct {
	max, used int64
	shared    *MemoryBudget
	head      *charge
	parent    *accounting
}
type charge struct {
	owner          *accounting
	token          *Reservation
	bytes          int64
	previous, next *charge
}

func (a *accounting) reserve(bytes int64) (*charge, error) {
	// Includes conservative space for charge and reservation bookkeeping.
	if bytes < 0 || bytes > a.max-a.used-128 {
		return nil, fmt.Errorf("%w: operation retained storage: request=%d overhead=128 used=%d limit=%d", ErrResourceLimit, bytes, a.used, a.max)
	}
	bytes += 128
	for scope := a.parent; scope != nil; scope = scope.parent {
		if bytes > scope.max-scope.used {
			return nil, fmt.Errorf("%w: parent retained storage: request=%d used=%d limit=%d", ErrResourceLimit, bytes, scope.used, scope.max)
		}
	}
	var token *Reservation
	var err error
	if a.shared != nil {
		token, err = a.shared.Reserve(bytes)
		if err != nil {
			return nil, err
		}
	}
	a.used += bytes
	for scope := a.parent; scope != nil; scope = scope.parent {
		scope.used += bytes
	}
	c := &charge{owner: a, token: token, bytes: bytes, next: a.head}
	if a.head != nil {
		a.head.previous = c
	}
	a.head = c
	return c, nil
}
func (c *charge) release() {
	if c == nil || c.owner == nil {
		return
	}
	a := c.owner
	if c.previous != nil {
		c.previous.next = c.next
	} else {
		a.head = c.next
	}
	if c.next != nil {
		c.next.previous = c.previous
	}
	a.used -= c.bytes
	for scope := a.parent; scope != nil; scope = scope.parent {
		scope.used -= c.bytes
	}
	c.token.Release()
	c.owner = nil
}
func (a *accounting) release() {
	for a.head != nil {
		a.head.release()
	}
}
