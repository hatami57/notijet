// Templates: notification content rendered from local template files
// embedded in the binary, with a locale variant and shared partials, then
// sent as email with an inline image. The logger provider prints the result,
// so it runs without credentials:
//
//	go run ./examples/templates
//
// tmpl.Decorate lets callers send a Message whose Template names a local
// template; it is rendered before the provider sees it, so the same code
// works with providers that have no templates of their own (SMTP here).
package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/logger"
	"github.com/hatami57/notijet/tmpl"
)

//go:embed templates
var files embed.FS

func main() {
	set, err := tmpl.ParseFS(files, "templates")
	if err != nil {
		log.Fatal(err)
	}
	data := map[string]any{"name": "Sara", "order": "1042", "eta": "Friday"}

	// Render directly.
	r, err := set.RenderLocale("order-shipped", "fa-IR", data)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("fa subject:", r.Subject)
	fmt.Println("fa text:   ", r.Text)

	// Or let a decorator render Message.Template for any provider. The
	// locale function reads the recipient's language from Metadata.
	out := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mail := logger.New(out, logger.WithName("dev-mail"), logger.WithChannel(notijet.ChannelEmail))
	p := tmpl.Decorate(mail, set, func(m *notijet.Message) string { return m.Metadata["locale"] })

	logo := []byte{0x89, 'P', 'N', 'G'} // stand-in image bytes
	_, err = p.Send(context.Background(), &notijet.Message{
		From:        "Acme <no-reply@acme.example>",
		To:          []string{"sara@example.com"},
		Template:    notijet.NewTemplate("order-shipped", "name", "Sara", "order", "1042", "eta", "Friday"),
		Attachments: []notijet.Attachment{{ContentID: "logo", ContentType: "image/png", Content: logo}},
		Metadata:    map[string]string{"locale": "en"},
	})
	if err != nil {
		log.Fatal(err)
	}
}
