package dbtmetrics

import _ "embed"

//go:embed macros/oxygen_cluster_rows.sql
var oxygenClusterMacro string

// OxygenClusterRowsSQL consumes the caller's date-scoped oxygen_samples CTE.
var OxygenClusterRowsSQL = macroBody(oxygenClusterMacro)
