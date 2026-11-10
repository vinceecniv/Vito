// Vito pill — the GNOME side of Vito's dictation pill.
//
// GNOME offers no protocol for a window that floats over every app without
// taking the keyboard, so Vito draws its pictures itself (the same ones the
// Wayland and Windows windows show) and this extension puts them on screen as
// shell chrome. Only the input boxes take the pointer; everything else lets
// clicks through. Shell chrome never takes the keyboard focus, so a paste still
// lands in the app it was meant for.
//
// Vito talks to it over D-Bus (talk.vito.Pill on the session bus). The
// extension holds no state of its own beyond the open pictures: when Vito goes
// away, they go too.

import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import St from 'gi://St';
import Clutter from 'gi://Clutter';
import Cogl from 'gi://Cogl';
import Meta from 'gi://Meta';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';

const BUS_NAME = 'talk.vito.Pill';
const OBJECT_PATH = '/talk/vito/Pill';
const VERSION = 5;

const IFACE = `<node><interface name="talk.vito.Pill">
  <property name="Version" type="u" access="read"/>
  <method name="Open">
    <arg type="s" name="kind" direction="in"/>
    <arg type="i" name="monitor" direction="in"/>
    <arg type="i" name="width" direction="in"/>
    <arg type="i" name="height" direction="in"/>
    <arg type="i" name="margin" direction="in"/>
    <arg type="(iiii)" name="input" direction="in"/>
    <arg type="u" name="id" direction="out"/>
    <arg type="i" name="placed_on" direction="out"/>
  </method>
  <method name="Present">
    <arg type="u" name="id" direction="in"/>
    <arg type="i" name="width" direction="in"/>
    <arg type="i" name="height" direction="in"/>
    <arg type="ay" name="pixels" direction="in"/>
  </method>
  <method name="SetInput">
    <arg type="u" name="id" direction="in"/>
    <arg type="(iiii)" name="input" direction="in"/>
  </method>
  <method name="SetInputs">
    <arg type="u" name="id" direction="in"/>
    <arg type="a(iiii)" name="inputs" direction="in"/>
  </method>
  <method name="Place">
    <arg type="u" name="id" direction="in"/>
    <arg type="b" name="absolute" direction="in"/>
    <arg type="i" name="a" direction="in"/>
    <arg type="i" name="b" direction="in"/>
  </method>
  <method name="SetCursor">
    <arg type="u" name="id" direction="in"/>
    <arg type="u" name="shape" direction="in"/>
  </method>
  <method name="Close">
    <arg type="u" name="id" direction="in"/>
  </method>
  <method name="Monitors">
    <arg type="a(iiiid)" name="monitors" direction="out"/>
  </method>
  <method name="Focus">
    <arg type="i" name="monitor" direction="out"/>
    <arg type="b" name="window" direction="out"/>
  </method>
  <signal name="Pointer">
    <arg type="u" name="id"/>
    <arg type="u" name="kind"/>
    <arg type="d" name="x"/>
    <arg type="d" name="y"/>
    <arg type="u" name="button"/>
  </signal>
  <signal name="Closed"><arg type="u" name="id"/></signal>
  <signal name="MonitorsChanged"/>
  <signal name="FocusChanged"><arg type="i" name="monitor"/></signal>
</interface></node>`;

// Pointer kinds and buttons, as Vito numbers them (internal/pill/pill.go).
const IN = 0, OUT = 1, MOVE = 2, CLICK = 3, RELEASE = 4, DELTA = 5;
const BUTTON_LEFT = 0x110, BUTTON_RIGHT = 0x111;
// Cursor shapes, as wp-cursor-shape-v1 numbers them.
const SHAPE_POINTER = 4, SHAPE_GRAB = 16, SHAPE_GRABBING = 17;

// The cursor over the input box. GNOME 48 and later set it per actor
// (set_cursor_type); before that the display shows one for everybody.
function clutterCursor(shape) {
    const T = Clutter.CursorType ?? {};
    switch (shape) {
    case SHAPE_POINTER:
        return T.POINTER ?? T.DEFAULT;
    case SHAPE_GRAB:
        return T.GRAB ?? T.MOVE ?? T.DEFAULT;
    case SHAPE_GRABBING:
        return T.GRABBING ?? T.MOVE ?? T.DEFAULT;
    }
    return T.DEFAULT;
}

function metaCursor(shape) {
    switch (shape) {
    case SHAPE_POINTER:
        return Meta.Cursor.POINTING_HAND;
    case SHAPE_GRAB:
    case SHAPE_GRABBING:
        return Meta.Cursor.MOVE_OR_RESIZE_WINDOW;
    }
    return Meta.Cursor.DEFAULT;
}

function showCursor(shape) {
    if (typeof global.display.set_cursor === 'function')
        global.display.set_cursor(metaCursor(shape));
}

// addChrome puts an actor above the windows. Up to GNOME 50 the shell keeps an
// input region of chrome that takes the pointer; from 51 it goes by which
// actor is reactive, and the parameter is gone.
function addChrome(actor, params) {
    try {
        Main.layoutManager.addTopChrome(actor, params);
    } catch (_e) {
        const {affectsInputRegion: _unused, ...rest} = params;
        Main.layoutManager.addTopChrome(actor, rest);
    }
}

class Picture {
    constructor(owner, id, kind, monitor, width, height, margin, input) {
        this.owner = owner;
        this.id = id;
        this.kind = kind;
        this.monitor = monitor;
        this.width = width;
        this.height = height;
        this.margin = margin;
        this.inputs = [input];
        this.boxes = [];
        this.idle = kind === 'idle';
        this.offX = 0;
        this.offUp = 0;
        this.absolute = false;
        this.left = 0;
        this.top = 0;
        this.shape = 0;
        this.inside = false;
        this.grab = null;
        this.last = null;

        // The picture never takes the pointer; the box over its input area does.
        this.actor = new St.Widget({reactive: false, width, height});
        this.content = St.ImageContent.new_with_preferred_size(width, height);
        this.actor.set_content(this.content);
        this.actor.set_content_scaling_filters(Clutter.ScalingFilter.TRILINEAR, Clutter.ScalingFilter.LINEAR);
        this.actor.opacity = 0; // until the first picture

        // The resting pill gives way to a fullscreen window; the rest stay up.
        addChrome(this.actor, {affectsInputRegion: false, trackFullscreen: this.idle});
        this.place();
    }

    // One reactive box per input rectangle (the island's panel, its
    // microphone, a card under it), so the free space between them lets the
    // pointer through to the windows below.
    syncBoxes() {
        while (this.boxes.length < this.inputs.length) {
            const box = new St.Widget({reactive: true, track_hover: true});
            addChrome(box, {affectsInputRegion: true, trackFullscreen: this.idle});
            box.connect('enter-event', (_a, e) => this.enter(e));
            box.connect('leave-event', () => this.leave());
            box.connect('motion-event', (_a, e) => this.motion(e));
            box.connect('button-press-event', (a, e) => this.press(a, e));
            box.connect('button-release-event', (_a, e) => this.release(e));
            if (typeof box.set_cursor_type === 'function')
                box.set_cursor_type(clutterCursor(this.shape));
            this.boxes.push(box);
        }
        while (this.boxes.length > this.inputs.length) {
            const box = this.boxes.pop();
            if (this.grab && this.grabbed === box) {
                this.grab.dismiss();
                this.grab = null;
            }
            Main.layoutManager.removeChrome(box);
            box.destroy();
        }
    }

    over(px, py, x, y) {
        return this.inputs.some(([bx, by, bw, bh]) =>
            px >= x + bx && px < x + bx + bw && py >= y + by && py < y + by + bh);
    }

    geometry() {
        const ms = Main.layoutManager.monitors;
        return ms[this.monitor] ?? Main.layoutManager.primaryMonitor ?? ms[0];
    }

    place() {
        const m = this.geometry();
        if (!m)
            return;
        let x, y;
        if (this.absolute) {
            x = m.x + this.left;
            y = m.y + this.top;
        } else {
            x = m.x + Math.round((m.width - this.width) / 2) + this.offX;
            y = m.y + m.height - this.margin - this.offUp - this.height;
        }
        this.actor.set_position(x, y);
        this.syncBoxes();
        this.inputs.forEach(([bx, by, bw, bh], i) => {
            this.boxes[i].set_position(x + bx, y + by);
            this.boxes[i].set_size(Math.max(bw, 0), Math.max(bh, 0));
        });
        this.hoverCheck(x, y);
    }

    // Clutter reports an enter only when the pointer moves: a box that appears
    // or moves under a still pointer has to look for itself.
    hoverCheck(x, y) {
        if (this.grab)
            return;
        const [px, py] = global.get_pointer();
        const over = this.over(px, py, x, y);
        if (over && !this.inside) {
            this.inside = true;
            showCursor(this.shape);
            this.owner.pointer(this.id, IN, px - x, py - y, 0);
        } else if (!over && this.inside) {
            this.leave();
        }
    }

    present(width, height, bytes) {
        const stride = width * 4;
        const format = Cogl.PixelFormat.BGRA_8888_PRE;
        try {
            // GNOME 46 and later take the Cogl context first.
            const ctx = global.stage.context.get_backend().get_cogl_context();
            this.content.set_bytes(ctx, bytes, format, width, height, stride);
        } catch (_e) {
            this.content.set_bytes(bytes, format, width, height, stride);
        }
        this.actor.opacity = 255;
    }

    local(e) {
        const [sx, sy] = e.get_coords();
        const [ax, ay] = this.actor.get_transformed_position();
        return [sx - ax, sy - ay, sx, sy];
    }

    enter(e) {
        if (this.inside)
            return Clutter.EVENT_PROPAGATE; // from one box straight into the next
        this.inside = true;
        showCursor(this.shape);
        const [x, y] = this.local(e);
        this.owner.pointer(this.id, IN, x, y, 0);
        return Clutter.EVENT_PROPAGATE;
    }

    leave() {
        if (this.grab || !this.inside)
            return Clutter.EVENT_PROPAGATE;
        // Into a box beside this one: still inside.
        const [px, py] = global.get_pointer();
        const [ax, ay] = this.actor.get_transformed_position();
        if (this.boxes.length > 1 && this.over(px, py, ax, ay))
            return Clutter.EVENT_PROPAGATE;
        this.inside = false;
        showCursor(0);
        this.owner.pointer(this.id, OUT, 0, 0, 0);
        return Clutter.EVENT_PROPAGATE;
    }

    motion(e) {
        const [x, y, sx, sy] = this.local(e);
        if (this.grab && this.last) {
            // During a drag the window moves under the pointer: its own motion
            // is what follows the pointer exactly.
            this.owner.pointer(this.id, DELTA, sx - this.last[0], sy - this.last[1], 0);
            this.last = [sx, sy];
        }
        this.owner.pointer(this.id, MOVE, x, y, 0);
        return Clutter.EVENT_STOP;
    }

    press(box, e) {
        const b = e.get_button() === 3 ? BUTTON_RIGHT : BUTTON_LEFT;
        this.button = b;
        const [x, y, sx, sy] = this.local(e);
        this.last = [sx, sy];
        try {
            this.grab = global.stage.grab(box);
            this.grabbed = box;
        } catch (_e) {
            this.grab = null;
        }
        this.owner.pointer(this.id, CLICK, x, y, b);
        return Clutter.EVENT_STOP;
    }

    release(e) {
        if (this.grab) {
            this.grab.dismiss();
            this.grab = null;
        }
        this.last = null;
        const [x, y] = this.local(e);
        this.owner.pointer(this.id, RELEASE, x, y, this.button ?? BUTTON_LEFT);
        if (!this.boxes.some(b => b.hover))
            this.leave();
        return Clutter.EVENT_STOP;
    }

    setCursor(shape) {
        this.shape = shape;
        if (this.boxes.length && typeof this.boxes[0].set_cursor_type === 'function')
            this.boxes.forEach(b => b.set_cursor_type(clutterCursor(shape)));
        else if (this.inside)
            showCursor(shape);
    }

    destroy() {
        if (this.grab) {
            this.grab.dismiss();
            this.grab = null;
        }
        if (this.inside)
            showCursor(0);
        for (const b of this.boxes) {
            Main.layoutManager.removeChrome(b);
            b.destroy();
        }
        this.boxes = [];
        Main.layoutManager.removeChrome(this.actor);
        this.actor.destroy();
    }
}

export default class VitoPillExtension extends Extension {
    enable() {
        this._pictures = new Map();
        this._next = 1;
        this._owners = new Map(); // picture id -> the D-Bus name that opened it
        this._dbus = Gio.DBusExportedObject.wrapJSObject(IFACE, this);
        this._dbus.export(Gio.DBus.session, OBJECT_PATH);
        this._nameId = Gio.bus_own_name_on_connection(Gio.DBus.session, BUS_NAME,
            Gio.BusNameOwnerFlags.REPLACE, null, null);
        this._monitorsId = Main.layoutManager.connect('monitors-changed', () => {
            for (const p of this._pictures.values())
                p.place();
            this._dbus.emit_signal('MonitorsChanged', null);
        });
        this._focusId = global.display.connect('notify::focus-window', () => {
            this._dbus.emit_signal('FocusChanged', new GLib.Variant('(i)', [this._focusMonitor()]));
        });
        // A picture whose owner left the bus goes with it.
        this._watchId = Gio.DBus.session.signal_subscribe('org.freedesktop.DBus',
            'org.freedesktop.DBus', 'NameOwnerChanged', '/org/freedesktop/DBus', null,
            Gio.DBusSignalFlags.NONE, (_c, _s, _p, _i, _n, params) => {
                const [name, , newOwner] = params.deep_unpack();
                if (newOwner !== '')
                    return;
                for (const [id, owner] of this._owners) {
                    if (owner === name)
                        this._close(id);
                }
            });
    }

    disable() {
        Gio.DBus.session.signal_unsubscribe(this._watchId);
        global.display.disconnect(this._focusId);
        Main.layoutManager.disconnect(this._monitorsId);
        for (const id of [...this._pictures.keys()])
            this._close(id);
        Gio.bus_unown_name(this._nameId);
        this._dbus.unexport();
        this._dbus = null;
        this._pictures = null;
        this._owners = null;
    }

    get Version() {
        return VERSION;
    }

    _focusMonitor() {
        const w = global.display.focus_window;
        return w ? w.get_monitor() : global.display.get_current_monitor();
    }

    _close(id) {
        const p = this._pictures.get(id);
        if (!p)
            return;
        this._pictures.delete(id);
        this._owners.delete(id);
        p.destroy();
        this._dbus?.emit_signal('Closed', new GLib.Variant('(u)', [id]));
    }

    pointer(id, kind, x, y, button) {
        this._dbus?.emit_signal('Pointer', new GLib.Variant('(uuddu)', [id, kind, x, y, button]));
    }

    // D-Bus methods. The async form gives the caller's name, so a picture can
    // follow its owner.

    OpenAsync(params, invocation) {
        const [kind, monitor, width, height, margin, input] = params;
        const ms = Main.layoutManager.monitors;
        const on = monitor >= 0 && monitor < ms.length ? monitor : this._focusMonitor();
        const id = this._next++;
        this._pictures.set(id, new Picture(this, id, kind, on, width, height, margin, input));
        this._owners.set(id, invocation.get_sender());
        invocation.return_value(new GLib.Variant('(ui)', [id, on]));
    }

    PresentAsync(params, invocation) {
        const [id, width, height] = params;
        const p = this._pictures.get(id);
        if (p) {
            // The bytes straight from the message, without unpacking them.
            const bytes = invocation.get_parameters().get_child_value(3).get_data_as_bytes();
            p.present(width, height, bytes);
        }
        invocation.return_value(null);
    }

    SetInput(id, input) {
        const p = this._pictures.get(id);
        if (p) {
            p.inputs = [input];
            p.place();
        }
    }

    SetInputs(id, inputs) {
        const p = this._pictures.get(id);
        if (p) {
            p.inputs = inputs.length ? inputs : [[0, 0, 0, 0]];
            p.place();
        }
    }

    Place(id, absolute, a, b) {
        const p = this._pictures.get(id);
        if (!p)
            return;
        p.absolute = absolute;
        if (absolute) {
            p.left = a;
            p.top = b;
        } else {
            p.offX = a;
            p.offUp = b;
        }
        p.place();
    }

    SetCursor(id, shape) {
        this._pictures.get(id)?.setCursor(shape);
    }

    Close(id) {
        this._close(id);
    }

    Monitors() {
        return Main.layoutManager.monitors.map(m =>
            [m.index, m.width, m.height, m.x, m.geometry_scale ?? 1]);
    }

    Focus() {
        return [this._focusMonitor(), global.display.focus_window !== null];
    }
}
