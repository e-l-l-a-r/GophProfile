// Package migrations содержит SQL-миграции базы данных, встроенные в
// исполняемые файлы. Благодаря встраиванию миграции применяются независимо от
// рабочего каталога.
package migrations

import "embed"

// FS - файловая система со всеми файлами *.up.sql и *.down.sql миграций.
//
//go:embed *.sql
var FS embed.FS
