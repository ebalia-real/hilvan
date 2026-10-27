package tray

import (
	"image"
	"image/color"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// fakeWatcher hace de bandeja (como la extensión AppIndicator de GNOME).
type fakeWatcher struct {
	mu    sync.Mutex
	items []string
}

func (w *fakeWatcher) RegisterStatusNotifierItem(service string) *dbus.Error {
	w.mu.Lock()
	w.items = append(w.items, service)
	w.mu.Unlock()
	return nil
}

func (w *fakeWatcher) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.items)
}

func startWatcher(t *testing.T) (*dbus.Conn, *fakeWatcher) {
	conn, err := dbus.ConnectSessionBus(dbus.WithSignalHandler(dbus.NewSequentialSignalHandler()))
	if err != nil {
		t.Fatal(err)
	}
	w := &fakeWatcher{}
	if err := conn.Export(w, watcherPath, watcherName); err != nil {
		t.Fatal(err)
	}
	if _, err := prop.Export(conn, watcherPath, prop.Map{watcherName: {
		"IsStatusNotifierHostRegistered": {Value: true, Emit: prop.EmitFalse},
	}}); err != nil {
		t.Fatal(err)
	}
	if r, err := conn.RequestName(watcherName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("no se pudo tomar el nombre de la bandeja: %v %v", r, err)
	}
	t.Cleanup(func() { _, _ = conn.ReleaseName(watcherName); conn.Close() })
	return conn, w
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no pasó: %s", what)
}

func TestTrayDBus(t *testing.T) {
	// Necesita un bus de sesión propio: dbus-run-session -- env HILVAN_TEST_PRIVATE_BUS=1 go test …
	if os.Getenv("HILVAN_TEST_PRIVATE_BUS") != "1" {
		t.Skip("solo en un bus de D-Bus privado (HILVAN_TEST_PRIVATE_BUS=1)")
	}
	wconn, w := startWatcher(t)

	icon := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for i := range icon.Pix {
		icon.Pix[i] = 0xff
	}
	var mu sync.Mutex
	var menuHits []int32
	var tokens []string
	tr, err := New("Hilvan", icon, []MenuItem{{ID: 1, Label: "Mostrar Hilvan"}, {ID: 2, Separator: true}, {ID: 3, Label: "Salir"}})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	tr.OnMenu = func(id int32) { mu.Lock(); menuHits = append(menuHits, id); mu.Unlock() }
	tr.OnActivate = func(tok string) { mu.Lock(); tokens = append(tokens, tok); mu.Unlock() }

	waitFor(t, "registro en la bandeja", func() bool { return w.count() == 1 })
	if uniqueNameOnly != strings.HasPrefix(w.items[0], ":") {
		t.Fatalf("nombre registrado: %s", w.items[0])
	}
	if !tr.Available() {
		t.Fatal("la bandeja figura no disponible")
	}

	// Lo que lee la bandeja.
	host, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	item := host.Object(w.items[0], itemPath)
	var all map[string]dbus.Variant
	if err := item.Call("org.freedesktop.DBus.Properties.GetAll", 0, itemIface).Store(&all); err != nil {
		t.Fatal(err)
	}
	// Los tipos tienen que ser los de la especificación: a(iiay) y (sa(iiay)ss).
	var pix []struct {
		W, H int32
		Data []byte
	}
	if err := all["IconPixmap"].Store(&pix); err != nil || len(pix) != 7 || pix[0].W != 16 || len(pix[0].Data) != 16*16*4 {
		t.Fatalf("IconPixmap: %v (%d)", err, len(pix))
	}
	var tip struct {
		Icon  string
		Image []struct {
			W, H int32
			Data []byte
		}
		Title, Text string
	}
	if err := all["ToolTip"].Store(&tip); err != nil || tip.Title != "Hilvan" {
		t.Fatalf("ToolTip: %v %+v", err, tip)
	}
	var sig string
	if err := item.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&sig); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name="IconPixmap" type="a(iiay)"`, `name="ToolTip" type="(sa(iiay)ss)"`, `name="Activate"`, `name="ProvideXdgActivationToken"`} {
		if !strings.Contains(sig, want) {
			t.Fatalf("la introspección no tiene %s", want)
		}
	}
	if all["Menu"].Value().(dbus.ObjectPath) != menuPath || all["Status"].Value().(string) != "Active" {
		t.Fatalf("propiedades: %v", all)
	}

	// Menú.
	menu := host.Object(w.items[0], menuPath)
	var rev uint32
	var root struct {
		ID       int32
		Props    map[string]dbus.Variant
		Children []dbus.Variant
	}
	if err := menu.Call(menuIface+".GetLayout", 0, int32(0), int32(-1), []string{}).Store(&rev, &root); err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 3 {
		t.Fatalf("menú con %d entradas", len(root.Children))
	}
	first := root.Children[0].Value().([]any)
	if label := first[1].(map[string]dbus.Variant)["label"].Value().(string); label != "Mostrar Hilvan" {
		t.Fatalf("primera entrada %q", label)
	}
	if err := menu.Call(menuIface+".Event", 0, int32(3), "clicked", dbus.MakeVariant(""), uint32(0)).Err; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "clic en Salir", func() bool { mu.Lock(); defer mu.Unlock(); return len(menuHits) == 1 && menuHits[0] == 3 })

	// Clic en el ícono, con token de activación de Wayland.
	if err := item.Call(itemIface+".ProvideXdgActivationToken", 0, "token-1").Err; err != nil {
		t.Fatal(err)
	}
	if err := item.Call(itemIface+".Activate", 0, int32(0), int32(0)).Err; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "activación", func() bool { mu.Lock(); defer mu.Unlock(); return len(tokens) == 1 && tokens[0] == "token-1" })

	// Punto de no leídos: avisa NewIcon y el píxel de arriba a la derecha queda rojo.
	_ = host.AddMatchSignal(dbus.WithMatchInterface(itemIface), dbus.WithMatchMember("NewIcon"))
	sigs := make(chan *dbus.Signal, 4)
	host.Signal(sigs)
	tr.SetBadge(true, "Hilvan", "2 chats sin leer")
	select {
	case <-sigs:
	case <-time.After(2 * time.Second):
		t.Fatal("no llegó NewIcon")
	}
	if err := item.Call("org.freedesktop.DBus.Properties.GetAll", 0, itemIface).Store(&all); err != nil {
		t.Fatal(err)
	}
	var pm []struct {
		W, H int32
		Data []byte
	}
	if err := all["IconPixmap"].Store(&pm); err != nil {
		t.Fatal(err)
	}
	for _, p := range pm {
		if p.W != 32 {
			continue
		}
		i := (5*32 + 26) * 4 // fila 5, columna 26: dentro del punto
		if a, r, g := p.Data[i], p.Data[i+1], p.Data[i+2]; a != 255 || r < 200 || g > 100 {
			t.Fatalf("el punto no es rojo: argb %d %d %d", a, r, g)
		}
	}

	// Si la bandeja se reinicia, el ícono se vuelve a registrar solo.
	if _, err := wconn.ReleaseName(watcherName); err != nil {
		t.Fatal(err)
	}
	if r, err := wconn.RequestName(watcherName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatal("no se pudo retomar el nombre")
	}
	waitFor(t, "nuevo registro", func() bool { return w.count() == 2 })
}

func TestRenderBadge(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			src.SetNRGBA(x, y, color.NRGBA{0x6d, 0x5d, 0xfc, 0xff})
		}
	}
	plain := Render(src, 22, false)
	if c := plain.NRGBAAt(20, 2); c.B < 200 || c.A != 255 {
		t.Fatalf("sin punto debería quedar el ícono: %v", c)
	}
	dot := Render(src, 22, true)
	if c := dot.NRGBAAt(17, 4); c.R < 200 || c.G > 100 || c.A < 250 {
		t.Fatalf("punto: %v", c)
	}
	if c := dot.NRGBAAt(4, 18); c != plain.NRGBAAt(4, 18) {
		t.Fatal("el resto del ícono no debería cambiar")
	}
	pm := toPixmap(dot)
	if pm.W != 22 || len(pm.Data) != 22*22*4 {
		t.Fatalf("pixmap %dx%d %d", pm.W, pm.H, len(pm.Data))
	}
}

// Dentro de flatpak no se puede tomar el nombre org.kde.StatusNotifierItem-…: la bandeja se
// registra con el nombre único de la conexión y todo tiene que andar igual.
func TestTrayDBusUniqueName(t *testing.T) {
	uniqueNameOnly = true
	defer func() { uniqueNameOnly = false }()
	TestTrayDBus(t)
}
