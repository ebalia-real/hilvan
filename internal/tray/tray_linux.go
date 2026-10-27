// Package tray muestra el ícono de Hilvan en la bandeja del sistema con su menú. Usa
// StatusNotifierItem + dbusmenu, el estándar de KDE, GNOME (con AppIndicator), Cinnamon,
// XFCE, Budgie, sway/waybar…: lo mismo que Element, Discord o Nheko.
package tray

import (
	"errors"
	"fmt"
	"image"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	itemPath    = dbus.ObjectPath("/StatusNotifierItem")
	menuPath    = dbus.ObjectPath("/MenuBar")
	itemIface   = "org.kde.StatusNotifierItem"
	menuIface   = "com.canonical.dbusmenu"
	watcherName = "org.kde.StatusNotifierWatcher"
	watcherPath = dbus.ObjectPath("/StatusNotifierWatcher")
)

type toolTip struct {
	Icon  string
	Image []Pixmap
	Title string
	Text  string
}

// MenuItem es una entrada del menú; ID 0 está reservado para la raíz.
type MenuItem struct {
	ID        int32
	Label     string
	Separator bool
	Check     bool // casilla de verificación
	Checked   bool
	Disabled  bool
}

// Tray es el ícono publicado en la sesión.
type Tray struct {
	conn  *dbus.Conn
	name  string
	props *prop.Properties
	mprop *prop.Properties

	mu     sync.Mutex
	base   image.Image
	badge  bool
	items  []MenuItem
	rev    uint32
	token  string
	closed bool

	// OnActivate se llama con el clic principal (token de activación de Wayland si lo hay).
	OnActivate func(token string)
	// OnMenu se llama al elegir una entrada del menú.
	OnMenu func(id int32)
}

// New publica el ícono. Si no hay bandeja todavía (por ejemplo GNOME sin la extensión
// AppIndicator) igual queda listo y se registra solo cuando aparezca.
func New(title string, icon image.Image, items []MenuItem) (*Tray, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	t := &Tray{conn: conn, base: icon, items: items, rev: 1}
	t.name = fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	if r, err := conn.RequestName(t.name, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		conn.Close()
		if err == nil {
			err = errors.New("nombre ocupado")
		}
		return nil, fmt.Errorf("bandeja: %w", err)
	}

	pix := t.pixmaps()
	itemProps := map[string]*prop.Prop{
		"Category":            {Value: "Communications", Emit: prop.EmitFalse},
		"Id":                  {Value: "hilvan", Emit: prop.EmitFalse},
		"Title":               {Value: title, Emit: prop.EmitFalse},
		"Status":              {Value: "Active", Emit: prop.EmitFalse},
		"WindowId":            {Value: int32(0), Emit: prop.EmitFalse},
		"IconName":            {Value: "", Emit: prop.EmitFalse},
		"IconThemePath":       {Value: "", Emit: prop.EmitFalse},
		"IconPixmap":          {Value: pix, Emit: prop.EmitFalse},
		"OverlayIconName":     {Value: "", Emit: prop.EmitFalse},
		"OverlayIconPixmap":   {Value: []Pixmap{}, Emit: prop.EmitFalse},
		"AttentionIconName":   {Value: "", Emit: prop.EmitFalse},
		"AttentionIconPixmap": {Value: []Pixmap{}, Emit: prop.EmitFalse},
		"AttentionMovieName":  {Value: "", Emit: prop.EmitFalse},
		"ToolTip":             {Value: toolTip{Title: title, Image: []Pixmap{}}, Emit: prop.EmitFalse},
		"ItemIsMenu":          {Value: false, Emit: prop.EmitFalse},
		"Menu":                {Value: menuPath, Emit: prop.EmitFalse},
	}
	if t.props, err = prop.Export(conn, itemPath, prop.Map{itemIface: itemProps}); err != nil {
		conn.Close()
		return nil, err
	}
	if err = conn.Export(itemObj{t}, itemPath, itemIface); err != nil {
		conn.Close()
		return nil, err
	}
	menuProps := map[string]*prop.Prop{
		"Version":       {Value: uint32(3), Emit: prop.EmitFalse},
		"TextDirection": {Value: "ltr", Emit: prop.EmitFalse},
		"Status":        {Value: "normal", Emit: prop.EmitFalse},
		"IconThemePath": {Value: []string{}, Emit: prop.EmitFalse},
	}
	if t.mprop, err = prop.Export(conn, menuPath, prop.Map{menuIface: menuProps}); err != nil {
		conn.Close()
		return nil, err
	}
	if err = conn.Export(menuObj{t}, menuPath, menuIface); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.Export(introspect.NewIntrospectable(&introspect.Node{Name: string(itemPath), Interfaces: []introspect.Interface{
		introspect.IntrospectData, prop.IntrospectData,
		{Name: itemIface, Methods: introspect.Methods(itemObj{}), Properties: t.props.Introspection(itemIface),
			Signals: []introspect.Signal{{Name: "NewIcon"}, {Name: "NewToolTip"}, {Name: "NewTitle"},
				{Name: "NewStatus", Args: []introspect.Arg{{Name: "status", Type: "s"}}}}},
	}}), itemPath, "org.freedesktop.DBus.Introspectable")
	_ = conn.Export(introspect.NewIntrospectable(&introspect.Node{Name: string(menuPath), Interfaces: []introspect.Interface{
		introspect.IntrospectData, prop.IntrospectData,
		{Name: menuIface, Methods: introspect.Methods(menuObj{}), Properties: t.mprop.Introspection(menuIface),
			Signals: []introspect.Signal{{Name: "LayoutUpdated", Args: []introspect.Arg{{Name: "revision", Type: "u"}, {Name: "parent", Type: "i"}}}}},
	}}), menuPath, "org.freedesktop.DBus.Introspectable")

	// Si la bandeja se reinicia (o aparece después), nos volvemos a registrar.
	_ = conn.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, watcherName))
	ch := make(chan *dbus.Signal, 8)
	conn.Signal(ch)
	go func() {
		for s := range ch {
			if s.Name == "org.freedesktop.DBus.NameOwnerChanged" && len(s.Body) == 3 {
				if owner, _ := s.Body[2].(string); owner != "" {
					_ = t.register()
				}
			}
		}
	}()
	_ = t.register()
	return t, nil
}

func (t *Tray) register() error {
	return t.conn.Object(watcherName, watcherPath).Call(watcherName+".RegisterStatusNotifierItem", 0, t.name).Err
}

// Available dice si hay una bandeja mostrando el ícono ahora.
func (t *Tray) Available() bool {
	if t == nil {
		return false
	}
	var has bool
	if err := t.conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, watcherName).Store(&has); err != nil || !has {
		return false
	}
	var registered bool
	v, err := t.conn.Object(watcherName, watcherPath).GetProperty(watcherName + ".IsStatusNotifierHostRegistered")
	if err == nil {
		registered, _ = v.Value().(bool)
		return registered
	}
	return true
}

// SetIcon cambia el ícono (por ejemplo al elegir otro estilo).
func (t *Tray) SetIcon(img image.Image) {
	t.mu.Lock()
	t.base = img
	t.mu.Unlock()
	t.updateIcon()
}

// SetBadge muestra u oculta el punto de mensajes sin leer y actualiza el texto de ayuda.
func (t *Tray) SetBadge(on bool, title, text string) {
	t.mu.Lock()
	changed := t.badge != on
	t.badge = on
	t.mu.Unlock()
	if changed {
		t.updateIcon()
	}
	t.props.SetMust(itemIface, "ToolTip", toolTip{Title: title, Text: text, Image: []Pixmap{}})
	_ = t.conn.Emit(itemPath, itemIface+".NewToolTip")
}

func (t *Tray) updateIcon() {
	t.props.SetMust(itemIface, "IconPixmap", t.pixmaps())
	_ = t.conn.Emit(itemPath, itemIface+".NewIcon")
}

// SetMenu reemplaza las entradas del menú.
func (t *Tray) SetMenu(items []MenuItem) {
	t.mu.Lock()
	t.items = items
	t.rev++
	rev := t.rev
	t.mu.Unlock()
	_ = t.conn.Emit(menuPath, menuIface+".LayoutUpdated", rev, int32(0))
}

// Close quita el ícono de la bandeja.
func (t *Tray) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
	_, _ = t.conn.ReleaseName(t.name)
	_ = t.conn.Close()
}

func (t *Tray) pixmaps() []Pixmap {
	t.mu.Lock()
	base, badge := t.base, t.badge
	t.mu.Unlock()
	if base == nil {
		return []Pixmap{}
	}
	var out []Pixmap
	for _, s := range []int{16, 22, 24, 32, 48, 64, 128} {
		out = append(out, toPixmap(Render(base, s, badge)))
	}
	return out
}

// --- org.kde.StatusNotifierItem ---

type itemObj struct{ t *Tray }

func (o itemObj) Activate(x, y int32) *dbus.Error {
	o.t.mu.Lock()
	tok := o.t.token
	o.t.token = ""
	f := o.t.OnActivate
	o.t.mu.Unlock()
	if f != nil {
		go f(tok)
	}
	return nil
}

func (o itemObj) SecondaryActivate(x, y int32) *dbus.Error { return o.Activate(x, y) }

// ContextMenu: las bandejas modernas muestran el menú dbusmenu por su cuenta.
func (o itemObj) ContextMenu(x, y int32) *dbus.Error { return nil }

func (o itemObj) Scroll(delta int32, orientation string) *dbus.Error { return nil }

// ProvideXdgActivationToken: en Wayland, permite que la ventana pase al frente al hacer clic.
func (o itemObj) ProvideXdgActivationToken(token string) *dbus.Error {
	o.t.mu.Lock()
	o.t.token = token
	o.t.mu.Unlock()
	return nil
}

// --- com.canonical.dbusmenu ---

type menuObj struct{ t *Tray }

type layout struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

type groupProps struct {
	ID    int32
	Props map[string]dbus.Variant
}

type menuEvent struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

func itemProps(it MenuItem) map[string]dbus.Variant {
	if it.Separator {
		return map[string]dbus.Variant{"type": dbus.MakeVariant("separator"), "visible": dbus.MakeVariant(true)}
	}
	p := map[string]dbus.Variant{
		"label":   dbus.MakeVariant(it.Label),
		"enabled": dbus.MakeVariant(!it.Disabled),
		"visible": dbus.MakeVariant(true),
	}
	if it.Check {
		state := int32(0)
		if it.Checked {
			state = 1
		}
		p["toggle-type"] = dbus.MakeVariant("checkmark")
		p["toggle-state"] = dbus.MakeVariant(state)
	}
	return p
}

func (m menuObj) GetLayout(parentID, depth int32, names []string) (uint32, layout, *dbus.Error) {
	m.t.mu.Lock()
	defer m.t.mu.Unlock()
	if parentID != 0 {
		for _, it := range m.t.items {
			if it.ID == parentID {
				return m.t.rev, layout{ID: it.ID, Props: itemProps(it), Children: []dbus.Variant{}}, nil
			}
		}
		return m.t.rev, layout{}, dbus.MakeFailedError(errors.New("no existe"))
	}
	root := layout{ID: 0, Props: map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")}, Children: []dbus.Variant{}}
	if depth != 0 {
		for _, it := range m.t.items {
			root.Children = append(root.Children, dbus.MakeVariant(layout{ID: it.ID, Props: itemProps(it), Children: []dbus.Variant{}}))
		}
	}
	return m.t.rev, root, nil
}

func (m menuObj) GetGroupProperties(ids []int32, names []string) ([]groupProps, *dbus.Error) {
	m.t.mu.Lock()
	defer m.t.mu.Unlock()
	out := []groupProps{}
	for _, it := range m.t.items {
		for _, id := range ids {
			if id == it.ID {
				out = append(out, groupProps{ID: it.ID, Props: itemProps(it)})
			}
		}
	}
	return out, nil
}

func (m menuObj) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	m.t.mu.Lock()
	defer m.t.mu.Unlock()
	for _, it := range m.t.items {
		if it.ID == id {
			if v, ok := itemProps(it)[name]; ok {
				return v, nil
			}
		}
	}
	return dbus.MakeVariant(""), dbus.MakeFailedError(errors.New("no existe"))
}

func (m menuObj) Event(id int32, eventID string, data dbus.Variant, ts uint32) *dbus.Error {
	if eventID == "clicked" {
		m.t.mu.Lock()
		f := m.t.OnMenu
		m.t.mu.Unlock()
		if f != nil {
			go f(id)
		}
	}
	return nil
}

func (m menuObj) EventGroup(events []menuEvent) ([]int32, *dbus.Error) {
	for _, e := range events {
		_ = m.Event(e.ID, e.EventID, e.Data, e.Timestamp)
	}
	return []int32{}, nil
}

func (m menuObj) AboutToShow(id int32) (bool, *dbus.Error) { return false, nil }

func (m menuObj) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return []int32{}, []int32{}, nil
}
