//go:build ignore

// XT-Nichrome Native Wayland/Hyprland Companion Cursor Overlay (wlr-layer-shell)
// Renders a 100% click-through second cursor with mood expressions, guiding
// subtext, and a floating chat bubble on the Wayland OVERLAY layer.
#include <gtk/gtk.h>
#include <gdk/gdk.h>
#include <gtk-layer-shell/gtk-layer-shell.h>
#include <cairo.h>
#include <pango/pangocairo.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <fcntl.h>

typedef struct {
    gboolean visible;
    gboolean pulse;
    double start_x, start_y;
    double cur_x, cur_y;
    double target_x, target_y;
    double ctrl_x, ctrl_y;
    double anim_t;      // 0.0 -> 1.0
    double pulse_phase; // 0.0 -> 1.0
    double bob_phase;
    char message[512];
    char subtext[160];
    char mood[32];
} CompanionState;

static CompanionState g_state = {
    .visible = TRUE,
    .pulse = TRUE,
    .start_x = 640.0,
    .start_y = 360.0,
    .cur_x = 640.0,
    .cur_y = 360.0,
    .target_x = 640.0,
    .target_y = 360.0,
    .ctrl_x = 640.0,
    .ctrl_y = 360.0,
    .anim_t = 1.0,
    .pulse_phase = 0.0,
    .bob_phase = 0.0,
    .message = "hey, I'm Ni! Ready to guide you.",
    .subtext = "Ni-28 companion",
    .mood = "friendly"
};

static GtkWidget *g_win = NULL;

static void apply_click_through(GtkWidget *widget) {
    GdkWindow *gdk_win = gtk_widget_get_window(widget);
    if (!gdk_win) return;
    cairo_region_t *empty = cairo_region_create();
    gdk_window_input_shape_combine_region(gdk_win, empty, 0, 0);
    cairo_region_destroy(empty);
}

static void on_realize(GtkWidget *widget, gpointer user_data) {
    (void)user_data;
    apply_click_through(widget);
}

static void on_size_allocate(GtkWidget *widget, GdkRectangle *alloc, gpointer user_data) {
    (void)alloc;
    (void)user_data;
    apply_click_through(widget);
}

static const char *mood_face(const char *m) {
    if (!m) return "(^‿^)";
    if (strcmp(m, "curious") == 0) return "(•_•?)";
    if (strcmp(m, "warn") == 0)    return "(•̀_•́)!";
    if (strcmp(m, "sad") == 0)     return "(´•︵•`)";
    if (strcmp(m, "excited") == 0) return "(✦‿✦)";
    if (strcmp(m, "thinking") == 0) return "(◔_◔)";
    return "(^‿^)";
}

static void rounded_rect(cairo_t *cr, double x, double y, double w, double h, double r) {
    double deg = M_PI / 180.0;
    cairo_new_sub_path(cr);
    cairo_arc(cr, x + w - r, y + r, r, -90 * deg, 0 * deg);
    cairo_arc(cr, x + w - r, y + h - r, r, 0 * deg, 90 * deg);
    cairo_arc(cr, x + r, y + h - r, r, 90 * deg, 180 * deg);
    cairo_arc(cr, x + r, y + r, r, 180 * deg, 270 * deg);
    cairo_close_path(cr);
}

static gboolean on_draw(GtkWidget *widget, cairo_t *cr, gpointer user_data) {
    (void)user_data;
    cairo_save(cr);
    cairo_set_operator(cr, CAIRO_OPERATOR_SOURCE);
    cairo_set_source_rgba(cr, 0.0, 0.0, 0.0, 0.0);
    cairo_paint(cr);
    cairo_restore(cr);

    if (!g_state.visible) {
        return FALSE;
    }

    int win_w = gtk_widget_get_allocated_width(widget);
    int win_h = gtk_widget_get_allocated_height(widget);
    if (win_w <= 0) win_w = 1920;
    if (win_h <= 0) win_h = 1080;

    double bob_y = sin(g_state.bob_phase) * 2.2;
    double cx = g_state.cur_x;
    double cy = g_state.cur_y + (g_state.anim_t >= 1.0 ? bob_y : 0.0);

    // 1. Expanding attention pulse rings around cursor tip
    if (g_state.pulse) {
        for (int i = 0; i < 2; i++) {
            double ph = fmod(g_state.pulse_phase + i * 0.5, 1.0);
            double rad = 8.0 + ph * 26.0;
            double alpha = (1.0 - ph) * 0.65;
            cairo_set_source_rgba(cr, 1.0, 0.18, 0.18, alpha);
            cairo_set_line_width(cr, 2.0);
            cairo_arc(cr, cx, cy, rad, 0, 2 * M_PI);
            cairo_stroke(cr);
        }
    }

    // 2. Draw XT-Nichrome Geometric Mini Cursor (sharp bevelled arrow)
    cairo_save(cr);
    cairo_translate(cr, cx, cy);

    // Soft drop shadow
    cairo_move_to(cr, 1.5, 3.0);
    cairo_line_to(cr, 23.5, 12.0);
    cairo_line_to(cr, 13.5, 15.5);
    cairo_line_to(cr, 10.5, 25.5);
    cairo_close_path(cr);
    cairo_set_source_rgba(cr, 0.0, 0.0, 0.0, 0.55);
    cairo_fill(cr);

    // Obsidian pointer body (#0A0A0A) + Alloy White border (#E8E6E3)
    cairo_move_to(cr, 0.0, 0.0);
    cairo_line_to(cr, 22.0, 9.0);
    cairo_line_to(cr, 12.0, 12.5);
    cairo_line_to(cr, 9.0, 22.5);
    cairo_close_path(cr);
    cairo_set_source_rgba(cr, 0.04, 0.04, 0.05, 0.96);
    cairo_fill_preserve(cr);
    cairo_set_source_rgba(cr, 0.91, 0.90, 0.89, 0.98);
    cairo_set_line_width(cr, 1.8);
    cairo_stroke(cr);

    // Crimson tip accent (#FF2E2E)
    cairo_move_to(cr, 0.0, 0.0);
    cairo_line_to(cr, 10.5, 4.3);
    cairo_line_to(cr, 6.8, 6.8);
    cairo_line_to(cr, 4.3, 10.5);
    cairo_close_path(cr);
    if (strcmp(g_state.mood, "warn") == 0 || strcmp(g_state.mood, "sad") == 0) {
        cairo_set_source_rgba(cr, 1.0, 0.18, 0.18, 1.0);
    } else {
        cairo_set_source_rgba(cr, 1.0, 0.18, 0.18, 0.95);
    }
    cairo_fill(cr);
    cairo_restore(cr);

    // 3. Draw Companion Chat Bubble & Guiding Subtext around the cursor
    if (g_state.message[0] != '\0' || g_state.subtext[0] != '\0') {
        PangoLayout *header_layout = pango_cairo_create_layout(cr);
        PangoFontDescription *hdr_font = pango_font_description_from_string("Segoe UI, Sans Bold 9.5");
        pango_layout_set_font_description(header_layout, hdr_font);

        char header_buf[256];
        const char *face = mood_face(g_state.mood);
        if (g_state.subtext[0] != '\0') {
            snprintf(header_buf, sizeof(header_buf), "Ni %s  •  %s", face, g_state.subtext);
        } else {
            snprintf(header_buf, sizeof(header_buf), "Ni %s  •  assistant", face);
        }
        pango_layout_set_text(header_layout, header_buf, -1);

        int hdr_w = 0, hdr_h = 0;
        pango_layout_get_pixel_size(header_layout, &hdr_w, &hdr_h);

        PangoLayout *msg_layout = pango_cairo_create_layout(cr);
        PangoFontDescription *msg_font = pango_font_description_from_string("Segoe UI, Sans 10.5");
        pango_layout_set_font_description(msg_layout, msg_font);
        pango_layout_set_width(msg_layout, 280 * PANGO_SCALE);
        pango_layout_set_wrap(msg_layout, PANGO_WRAP_WORD_CHAR);
        pango_layout_set_text(msg_layout, g_state.message, -1);

        int msg_w = 0, msg_h = 0;
        if (g_state.message[0] != '\0') {
            pango_layout_get_pixel_size(msg_layout, &msg_w, &msg_h);
        }

        double pad_x = 12.0, pad_y = 9.0;
        double content_w = hdr_w > msg_w ? hdr_w : msg_w;
        if (content_w < 130.0) content_w = 130.0;
        double card_w = content_w + pad_x * 2 + 4.0;
        double card_h = hdr_h + (msg_h > 0 ? (msg_h + 5.0) : 0.0) + pad_y * 2;

        double bx = cx + 22.0;
        double by = cy + 14.0;
        if (bx + card_w + 16.0 > win_w) {
            bx = cx - card_w - 12.0;
        }
        if (by + card_h + 16.0 > win_h) {
            by = cy - card_h - 10.0;
        }
        if (bx < 8.0) bx = 8.0;
        if (by < 8.0) by = 8.0;

        // Card drop shadow
        rounded_rect(cr, bx + 2.0, by + 4.0, card_w, card_h, 9.0);
        cairo_set_source_rgba(cr, 0.0, 0.0, 0.0, 0.50);
        cairo_fill(cr);

        // Card obsidian background (#0A0A0C) + border (#2E2E36)
        rounded_rect(cr, bx, by, card_w, card_h, 9.0);
        cairo_set_source_rgba(cr, 0.04, 0.04, 0.05, 0.94);
        cairo_fill_preserve(cr);
        cairo_set_source_rgba(cr, 0.22, 0.22, 0.26, 0.95);
        cairo_set_line_width(cr, 1.4);
        cairo_stroke(cr);

        // Left accent stripe (crimson for friendly/warn, amber/blue for thinking/sad)
        rounded_rect(cr, bx + 1.0, by + 2.0, 3.5, card_h - 4.0, 2.0);
        if (strcmp(g_state.mood, "sad") == 0) {
            cairo_set_source_rgba(cr, 0.98, 0.45, 0.09, 1.0);
        } else if (strcmp(g_state.mood, "curious") == 0 || strcmp(g_state.mood, "thinking") == 0) {
            cairo_set_source_rgba(cr, 0.23, 0.51, 0.96, 1.0);
        } else {
            cairo_set_source_rgba(cr, 1.0, 0.18, 0.18, 1.0);
        }
        cairo_fill(cr);

        // Render header text
        cairo_move_to(cr, bx + pad_x + 3.0, by + pad_y);
        cairo_set_source_rgba(cr, 1.0, 0.32, 0.32, 1.0);
        pango_cairo_show_layout(cr, header_layout);

        // Render chat bubble message text
        if (msg_h > 0) {
            cairo_move_to(cr, bx + pad_x + 3.0, by + pad_y + hdr_h + 4.0);
            cairo_set_source_rgba(cr, 0.95, 0.95, 0.96, 1.0);
            pango_cairo_show_layout(cr, msg_layout);
        }

        pango_font_description_free(hdr_font);
        pango_font_description_free(msg_font);
        g_object_unref(header_layout);
        g_object_unref(msg_layout);
    }

    return FALSE;
}

// Minimal JSON field extractors for newline-delimited commands from Go parent.
static gboolean extract_double(const char *json, const char *key, double *out) {
    char pat[64];
    snprintf(pat, sizeof(pat), "\"%s\":", key);
    const char *p = strstr(json, pat);
    if (!p) return FALSE;
    p += strlen(pat);
    while (*p == ' ') p++;
    if (*p == 'n') return FALSE; // null
    char *end = NULL;
    double v = strtod(p, &end);
    if (end == p) return FALSE;
    *out = v;
    return TRUE;
}

static gboolean extract_bool(const char *json, const char *key, gboolean *out) {
    char pat[64];
    snprintf(pat, sizeof(pat), "\"%s\":", key);
    const char *p = strstr(json, pat);
    if (!p) return FALSE;
    p += strlen(pat);
    while (*p == ' ') p++;
    if (strncmp(p, "true", 4) == 0) { *out = TRUE; return TRUE; }
    if (strncmp(p, "false", 5) == 0) { *out = FALSE; return TRUE; }
    return FALSE;
}

static gboolean extract_string(const char *json, const char *key, char *out, size_t cap) {
    char pat[64];
    snprintf(pat, sizeof(pat), "\"%s\":\"", key);
    const char *p = strstr(json, pat);
    if (!p) return FALSE;
    p += strlen(pat);
    size_t i = 0;
    while (*p && *p != '"' && i + 1 < cap) {
        if (*p == '\\' && *(p + 1)) {
            p++;
            if (*p == 'n') out[i++] = ' ';
            else out[i++] = *p;
        } else {
            out[i++] = *p;
        }
        p++;
    }
    out[i] = '\0';
    return TRUE;
}

static void handle_json_line(const char *line) {
    if (strstr(line, "\"quit\":true")) {
        gtk_main_quit();
        return;
    }
    gboolean vis = g_state.visible;
    if (extract_bool(line, "visible", &vis)) {
        g_state.visible = vis;
    }
    gboolean pls = g_state.pulse;
    if (extract_bool(line, "pulse", &pls)) {
        g_state.pulse = pls;
    }
    double tx = g_state.target_x, ty = g_state.target_y;
    gboolean has_x = extract_double(line, "x", &tx);
    gboolean has_y = extract_double(line, "y", &ty);
    if ((has_x || has_y) && (fabs(tx - g_state.cur_x) > 1.0 || fabs(ty - g_state.cur_y) > 1.0)) {
        g_state.start_x = g_state.cur_x;
        g_state.start_y = g_state.cur_y;
        g_state.target_x = tx;
        g_state.target_y = ty;
        double dx = tx - g_state.start_x;
        double dy = ty - g_state.start_y;
        // Slight perpendicular arc for human-like companion glide
        g_state.ctrl_x = g_state.start_x + dx * 0.5 - dy * 0.14;
        g_state.ctrl_y = g_state.start_y + dy * 0.5 + dx * 0.14;
        g_state.anim_t = 0.0;
    }
    extract_string(line, "message", g_state.message, sizeof(g_state.message));
    extract_string(line, "subtext", g_state.subtext, sizeof(g_state.subtext));
    extract_string(line, "mood", g_state.mood, sizeof(g_state.mood));
    if (g_win) {
        gtk_widget_queue_draw(g_win);
    }
}

static gboolean on_stdin_ready(GIOChannel *source, GIOCondition cond, gpointer data) {
    (void)data;
    if (cond & (G_IO_HUP | G_IO_ERR)) {
        gtk_main_quit();
        return FALSE;
    }
    gchar *line = NULL;
    gsize len = 0;
    GError *err = NULL;
    while (g_io_channel_read_line(source, &line, &len, NULL, &err) == G_IO_STATUS_NORMAL && line) {
        handle_json_line(line);
        g_free(line);
        line = NULL;
    }
    if (err) g_error_free(err);
    return TRUE;
}

static gboolean on_tick(gpointer data) {
    (void)data;
    if (!g_state.visible) return TRUE;

    gboolean redraw = FALSE;
    if (g_state.anim_t < 1.0) {
        g_state.anim_t += 0.045;
        if (g_state.anim_t >= 1.0) {
            g_state.anim_t = 1.0;
            g_state.cur_x = g_state.target_x;
            g_state.cur_y = g_state.target_y;
        } else {
            double u = g_state.anim_t;
            // Minimum-jerk velocity profile: s(u) = 10u^3 - 15u^4 + 6u^5
            double s = 10.0 * u * u * u - 15.0 * u * u * u * u + 6.0 * u * u * u * u * u;
            double inv = 1.0 - s;
            g_state.cur_x = inv * inv * g_state.start_x + 2.0 * inv * s * g_state.ctrl_x + s * s * g_state.target_x;
            g_state.cur_y = inv * inv * g_state.start_y + 2.0 * inv * s * g_state.ctrl_y + s * s * g_state.target_y;
        }
        redraw = TRUE;
    }
    g_state.bob_phase += 0.09;
    if (g_state.pulse) {
        g_state.pulse_phase = fmod(g_state.pulse_phase + 0.022, 1.0);
    }
    redraw = TRUE;

    if (redraw && g_win) {
        gtk_widget_queue_draw(g_win);
    }
    return TRUE;
}

int main(int argc, char **argv) {
    gtk_init(&argc, &argv);

    g_win = gtk_window_new(GTK_WINDOW_TOPLEVEL);
    gtk_window_set_title(GTK_WINDOW(g_win), "xt-nichrome-companion-cursor");
    gtk_window_set_decorated(GTK_WINDOW(g_win), FALSE);
    gtk_widget_set_app_paintable(g_win, TRUE);

    GdkScreen *screen = gtk_widget_get_screen(g_win);
    GdkVisual *visual = gdk_screen_get_rgba_visual(screen);
    if (visual) {
        gtk_widget_set_visual(g_win, visual);
    }

    if (gtk_layer_is_supported()) {
        gtk_layer_init_for_window(GTK_WINDOW(g_win));
        gtk_layer_set_layer(GTK_WINDOW(g_win), GTK_LAYER_SHELL_LAYER_OVERLAY);
        gtk_layer_set_namespace(GTK_WINDOW(g_win), "xt-nichrome-cursor");
        gtk_layer_set_keyboard_mode(GTK_WINDOW(g_win), GTK_LAYER_SHELL_KEYBOARD_MODE_NONE);
        gtk_layer_set_exclusive_zone(GTK_WINDOW(g_win), -1);
        gtk_layer_set_anchor(GTK_WINDOW(g_win), GTK_LAYER_SHELL_EDGE_TOP, TRUE);
        gtk_layer_set_anchor(GTK_WINDOW(g_win), GTK_LAYER_SHELL_EDGE_BOTTOM, TRUE);
        gtk_layer_set_anchor(GTK_WINDOW(g_win), GTK_LAYER_SHELL_EDGE_LEFT, TRUE);
        gtk_layer_set_anchor(GTK_WINDOW(g_win), GTK_LAYER_SHELL_EDGE_RIGHT, TRUE);
    } else {
        gtk_window_set_default_size(GTK_WINDOW(g_win), 1920, 1080);
        gtk_window_set_keep_above(GTK_WINDOW(g_win), TRUE);
        gtk_window_set_accept_focus(GTK_WINDOW(g_win), FALSE);
    }

    g_signal_connect(g_win, "realize", G_CALLBACK(on_realize), NULL);
    g_signal_connect(g_win, "size-allocate", G_CALLBACK(on_size_allocate), NULL);
    g_signal_connect(g_win, "draw", G_CALLBACK(on_draw), NULL);
    g_signal_connect(g_win, "destroy", G_CALLBACK(gtk_main_quit), NULL);

    int flags = fcntl(STDIN_FILENO, F_GETFL, 0);
    fcntl(STDIN_FILENO, F_SETFL, flags | O_NONBLOCK);
    GIOChannel *ch = g_io_channel_unix_new(STDIN_FILENO);
    g_io_channel_set_encoding(ch, NULL, NULL);
    g_io_add_watch(ch, G_IO_IN | G_IO_HUP | G_IO_ERR, on_stdin_ready, NULL);

    g_timeout_add(16, on_tick, NULL);

    gtk_widget_show_all(g_win);
    apply_click_through(g_win);
    gtk_main();
    return 0;
}
