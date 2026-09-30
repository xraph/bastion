package extension

import (
	dashboard "github.com/xraph/forge/extensions/dashboard"
)

// The dashboard finds bastion's contract contributor by runtime type
// assertion, so production code need not import forge's dashboard root. This
// keeps the method checked against the real interface anyway.
var _ dashboard.ContractContributorAware = (*Extension)(nil)
