package kilo

import "errors"

// maxPasteBytes bounds raw clipboard capture, not file or document size.
const maxPasteBytes = 1 << 20

// ErrPasteTooLarge reports clipboard input exceeding the raw capture bound.
// It may be wrapped in RecoveryError when unsaved source has been recovered.
var ErrPasteTooLarge = errors.New("pasted text exceeds 1 MiB raw input")
