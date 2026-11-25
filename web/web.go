// Package web содержит ресурсы веб-интерфейса, встроенные в исполняемый файл:
// статическую страницу загрузки (static/index.html из GophProfileUI) и
// HTML-шаблоны галереи и страницы ошибки.
package web

import (
	"embed"
	"html/template"
	"io/fs"
)

//go:embed static templates
var files embed.FS

// Static - статические файлы веб-интерфейса (корень - каталог static).
var Static = mustSub(files, "static")

// Templates - шаблоны страниц: gallery.html и error.html. Шаблоны встроены в
// исполняемый файл, поэтому ошибка разбора - ошибка сборки, её ловят тесты.
var Templates = template.Must(template.ParseFS(files, "templates/*.html"))

// mustSub возвращает подкаталог dir встроенной файловой системы.
func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}

	return sub
}
