package domain

import "errors"

// The vocabulary a use case has to speak about storage without knowing there is
// any. A repository returns these; the transport maps them to status codes.
var (
	// ErrNotFound is a row that is not there.
	ErrNotFound = errors.New("not found")

	// ErrNameTaken is the one collision a user can do something about.
	ErrNameTaken = errors.New("that name is taken")

	// ErrConflict is a contract that moved on between being read and being
	// written. It is expected, not exceptional: two workers racing for the
	// same contract is the mechanism working, and one of them losing is how.
	ErrConflict = errors.New("the contract is no longer in the state it was read in")
)

// Lost says whether an error means someone else got there first.
//
// There are two ways to lose the same race: the row moved between the read and
// the write, or it had already moved before the read, so the transition was
// refused outright. Both are a 409 and neither is worth retrying without
// re-reading — and a caller should not have to tell them apart to know that.
func Lost(err error) bool {
	return errors.Is(err, ErrConflict) || errors.Is(err, ErrTransition)
}
