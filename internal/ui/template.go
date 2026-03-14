package ui

// indexHTML is assembled from separate parts: style.go (CSS), html.go (HTML body), script.go (JS).
var indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Health</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4/dist/chart.umd.min.js"></script>
<style>` + cssStyle + `</style>
</head>
<body>` + htmlBody + `<script>` + jsI18N + jsState + jsDashboard + jsUI + jsCharts + jsSection + jsMetrics + jsAdmin + jsImport + `</script>
</body>
</html>`
