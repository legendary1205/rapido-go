package telegrambot

import "fmt"

// tr renders a catalog entry. A key missing from the chosen language falls back
// to English, then to the key itself, so a gap shows up as text rather than a
// blank message.
func tr(l lang, key string, args ...any) string {
	s, ok := catalogs[l][key]
	if !ok {
		s, ok = catalogs[langEN][key]
	}
	if !ok {
		return key
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

func hasKey(key string) bool {
	_, ok := catalogs[langEN][key]
	return ok
}

var catalogs = map[lang]map[string]string{
	langFA: {
		"btn.home":    "🏠 خانه",
		"btn.back":    "🔙 بازگشت",
		"btn.cancel":  "✖️ انصراف",
		"btn.yes":     "✅ بله",
		"btn.refresh": "🔄 بروزرسانی",
		"btn.custom":  "✏️ دلخواه",
		"btn.system":  "📊 سیستم",
		"btn.users":   "👥 کاربران",
		"btn.newuser": "➕ کاربر جدید",
		"btn.nodes":   "🖥 نودها",
		"btn.backup":  "💾 پشتیبان‌گیری",
		"btn.help":    "❓ راهنما",
		"btn.lang":    "🌐 English",

		"role.sudo":  "مدیر ارشد",
		"role.admin": "ادمین",

		"refuse": "⛔ شما اجازهٔ استفاده از این ربات را ندارید.",

		"home.title": "🏠 <b>کنسول مدیریت Rapido</b>\nوارد شده‌اید با <code>%s</code> · %s\n\nیک گزینه را انتخاب کنید یا نام کاربری را برای جستجو بفرستید.",
		"help.body": "❓ <b>راهنما</b>\n\n" +
			"• نام کاربری (یا بخشی از آن) را بفرستید تا جستجو شود.\n" +
			"• «کاربران» فهرست کاربران را با فیلتر وضعیت نشان می‌دهد.\n" +
			"• «کاربر جدید» با چند قدم کاربر می‌سازد.\n" +
			"• روی کارت هر کاربر: فعال/غیرفعال، ریست مصرف، تمدید، افزایش حجم، QR، یادداشت، لغو لینک و حذف.\n" +
			"• /start منوی اصلی را باز می‌کند و /cancel کار نیمه‌کاره را لغو می‌کند.",
		"help.sudo": "• «نودها» وضعیت سرورها و تونل‌های WireGuard را نشان می‌دهد.\n• «پشتیبان‌گیری» از پایگاه‌داده نسخهٔ پشتیبان می‌سازد و همین‌جا می‌فرستد.",

		"st.active":   "فعال",
		"st.disabled": "غیرفعال",
		"st.limited":  "حجم تمام‌شده",
		"st.expired":  "منقضی",
		"st.on_hold":  "در انتظار",

		"ns.connected":  "متصل",
		"ns.connecting": "در حال اتصال",
		"ns.error":      "خطا",
		"ns.disabled":   "غیرفعال",

		"err.generic":      "خطایی رخ داد. دوباره تلاش کنید.",
		"err.auth":         "حساب ادمین شما دیگر معتبر نیست.",
		"err.forbidden":    "دسترسی لازم را ندارید.",
		"err.usernotfound": "کاربر پیدا نشد.",
		"in.num_invalid":   "عدد معتبر نیست. دوباره بفرستید.",
		"in.toolong":       "پیام خیلی طولانی است.",

		"sys.title":        "📊 <b>وضعیت سیستم</b>",
		"sys.own_only":     "(فقط کاربران شما)",
		"sys.users":        "👥 کاربران: <b>%d</b>",
		"sys.by_status":    "🟢 فعال %d · 🔴 غیرفعال %d · 🟠 حجم‌تمام %d · ⌛ منقضی %d · ⏸ در انتظار %d",
		"sys.online":       "🟢 آنلاین: <b>%d</b>",
		"sys.traffic":      "📈 ترافیک کل: ↑ %s · ↓ %s",
		"sys.speed":        "⚡ سرعت: ↑ %s/s · ↓ %s/s",
		"sys.panel":        "🖥 پنل: CPU %s · RAM %s از %s",
		"sys.nodes":        "🌐 نودها: <b>%d</b> از %d متصل",
		"sys.tunnels_ok":   "🔐 WireGuard: همهٔ تونل‌ها سالم‌اند",
		"sys.tunnels_bad":  "⚠️ WireGuard: %d تونل مشکل دارد",
		"nodes.title":      "🖥 <b>نودها</b>",
		"nodes.panel_line": "🖥 <b>پنل</b>",
		"nodes.none":       "هیچ نودی ثبت نشده است.",
		"nodes.status":     "وضعیت: %s",
		"nodes.stale":      "⚠️ گزارش قدیمی است",
		"nodes.nometrics":  "هنوز گزارشی نرسیده است",
		"nodes.more":       "… و %d نود دیگر",
		"wg.down":          "قطع",
		"wg.missing":       "وجود ندارد",

		"users.menu":        "👥 <b>کاربران</b>\nیک فیلتر انتخاب کنید، یا بخشی از نام کاربری را بفرستید تا جستجو شود.",
		"flt.all":           "همه",
		"list.title":        "👥 <b>کاربران</b> · %s",
		"list.search_title": "🔎 <b>نتایج جستجو</b> «%s»",
		"list.count":        "%d کاربر · صفحهٔ %d از %d",
		"list.empty":        "کاربری پیدا نشد.",

		"card.usage":         "📦 مصرف: %s / %s",
		"card.unlimited":     "نامحدود",
		"card.expire":        "📅 انقضا: %s (%s)",
		"card.expire_never":  "📅 انقضا: نامحدود",
		"card.days_left":     "%d روز مانده",
		"card.expired_ago":   "%d روز پیش منقضی شد",
		"card.onhold":        "⏸ در انتظار اولین اتصال (%d روز پس از اتصال)",
		"card.online":        "🕐 آخرین اتصال: %s",
		"card.online_never":  "🕐 آخرین اتصال: هرگز",
		"card.owner":         "🧑‍💼 مالک: %s",
		"card.owner_none":    "پنل (بدون ادمین)",
		"card.note":          "📝 یادداشت: %s",
		"card.synced":        "🔁 این کاربر توسط پنل «%s» مدیریت می‌شود",
		"card.links":         "🔗 لینک اشتراک:",
		"card.nolinks":       "🔗 لینک اشتراک در دسترس نیست.",
		"card.nolinks_short": "لینکی در دسترس نیست",
		"ago.now":            "همین حالا",
		"ago.min":            "%d دقیقه پیش",
		"ago.hour":           "%d ساعت پیش",
		"ago.day":            "%d روز پیش",

		"act.disable": "🔴 غیرفعال‌سازی",
		"act.enable":  "🟢 فعال‌سازی",
		"act.reset":   "♻️ ریست مصرف",
		"act.extend":  "📅 تمدید",
		"act.data":    "📦 افزایش حجم",
		"act.qr":      "📷 QR",
		"act.note":    "📝 یادداشت",
		"act.revoke":  "🔗 لغو لینک",
		"act.delete":  "🗑 حذف",

		"confirm.reset":  "♻️ مصرف %s صفر شود؟",
		"confirm.revoke": "🔗 لینک اشتراک %s لغو و لینک تازه ساخته شود؟ لینک و کانفیگ‌های فعلی از کار می‌افتند.",
		"confirm.delete": "🗑 کاربر %s برای همیشه حذف شود؟",

		"done.disabled":     "کاربر غیرفعال شد.",
		"done.enabled":      "کاربر فعال شد.",
		"done.reset":        "مصرف صفر شد.",
		"done.revoked":      "لینک اشتراک لغو و لینک تازه ساخته شد.",
		"done.deleted":      "حذف شد.",
		"done.deleted_user": "🗑 کاربر %s حذف شد.",
		"done.extended":     "%d روز به انقضا اضافه شد.",
		"done.data":         "%s به حجم اضافه شد.",
		"done.note":         "یادداشت ذخیره شد.",
		"done.qr":           "کد QR ارسال شد.",

		"ext.title":     "📅 <b>تمدید %s</b>\nچند روز اضافه شود؟",
		"ext.prompt":    "تعداد روزی که باید اضافه شود را بفرستید (۱ تا ۳۶۵۰).",
		"ext.unlimited": "این کاربر تاریخ انقضا ندارد؛ چیزی برای تمدید نیست.",
		"dat.title":     "📦 <b>افزایش حجم %s</b>\nچقدر اضافه شود؟",
		"dat.prompt":    "مقدار حجم را به گیگابایت بفرستید، مثلاً 5 یا 2.5.",
		"dat.unlimited": "این کاربر حجم نامحدود دارد؛ چیزی برای افزودن نیست.",
		"note.prompt":   "یادداشت جدید را بفرستید (حداکثر ۵۰۰ بایت). برای پاک کردن یادداشت، «-» بفرستید.",
		"note.toolong":  "یادداشت بیش از ۵۰۰ بایت است.",

		"nu.name":              "➕ <b>کاربر جدید</b>\nنام کاربری را بفرستید (۳ تا ۳۲ کاراکتر: حروف انگلیسی، عدد و _ ).",
		"nu.name_invalid":      "نام کاربری معتبر نیست: ۳ تا ۳۲ کاراکتر از حروف انگلیسی، عدد و _ .",
		"nu.tmpl":              "➕ %s\nیک قالب انتخاب کنید یا بدون قالب ادامه دهید.",
		"nu.no_tmpl":           "بدون قالب (تنظیم دستی)",
		"nu.limit":             "➕ %s\n📦 حجم را به گیگابایت انتخاب کنید یا عدد را بفرستید (∞ = نامحدود).",
		"nu.days":              "➕ %s\n📅 مدت اشتراک را به روز انتخاب کنید یا عدد را بفرستید (∞ = نامحدود).",
		"nu.days_short":        "%d روز",
		"nu.confirm":           "✅ <b>تأیید ساخت کاربر</b>",
		"nu.summary_tmpl":      "🧩 قالب: %s",
		"nu.summary_limit":     "📦 حجم: %s",
		"nu.summary_life":      "📅 مدت: %s",
		"nu.summary_protocols": "🔌 پروتکل‌ها: %s",
		"nu.create":            "✅ ساخت کاربر",
		"nu.created":           "کاربر ساخته شد.",
		"nu.expired":           "این مرحله منقضی شد. دوباره از «کاربر جدید» شروع کنید.",
		"nu.no_inbounds":       "هنوز هیچ اینباندی روی پنل تعریف نشده است.",
		"nu.change_name":       "✏️ تغییر نام کاربری",
		"nu.usebuttons":        "برای این مرحله از دکمه‌ها استفاده کنید.",

		"bk.confirm":       "💾 از پایگاه‌داده نسخهٔ پشتیبان ساخته و برای شما ارسال شود؟",
		"bk.create":        "💾 ساخت و ارسال",
		"bk.working":       "⏳ در حال ساخت نسخهٔ پشتیبان…",
		"bk.working_short": "در حال ساخت…",
		"bk.done":          "نسخهٔ پشتیبان ارسال شد.",
		"bk.toolarge":      "فایل پشتیبان (%s) برای ارسال از تلگرام بزرگ است؛ آن را از داشبورد دانلود کنید.",
		"bk.fail":          "ساخت نسخهٔ پشتیبان ناموفق بود: %s",
		"bk.sendfail":      "نسخهٔ پشتیبان ساخته شد ولی ارسال آن به تلگرام ناموفق بود.",
	},
	langEN: {
		"btn.home":    "🏠 Home",
		"btn.back":    "🔙 Back",
		"btn.cancel":  "✖️ Cancel",
		"btn.yes":     "✅ Yes",
		"btn.refresh": "🔄 Refresh",
		"btn.custom":  "✏️ Custom",
		"btn.system":  "📊 System",
		"btn.users":   "👥 Users",
		"btn.newuser": "➕ New user",
		"btn.nodes":   "🖥 Nodes",
		"btn.backup":  "💾 Backup",
		"btn.help":    "❓ Help",
		"btn.lang":    "🌐 فارسی",

		"role.sudo":  "sudo",
		"role.admin": "admin",

		"refuse": "⛔ You are not allowed to use this bot.",

		"home.title": "🏠 <b>Rapido admin console</b>\nSigned in as <code>%s</code> · %s\n\nPick an option, or send a username to search.",
		"help.body": "❓ <b>Help</b>\n\n" +
			"• Send a username (or part of one) to search.\n" +
			"• Users lists everyone, filtered by status.\n" +
			"• New user walks you through creating one.\n" +
			"• On a user card: enable/disable, reset usage, extend, add data, QR, note, revoke link and delete.\n" +
			"• /start opens the main menu and /cancel abandons whatever you were doing.",
		"help.sudo": "• Nodes shows server status and WireGuard tunnel health.\n• Backup creates a database backup and sends it here.",

		"st.active":   "Active",
		"st.disabled": "Disabled",
		"st.limited":  "Data used up",
		"st.expired":  "Expired",
		"st.on_hold":  "On hold",

		"ns.connected":  "connected",
		"ns.connecting": "connecting",
		"ns.error":      "error",
		"ns.disabled":   "disabled",

		"err.generic":      "Something went wrong. Please try again.",
		"err.auth":         "Your admin account is no longer valid.",
		"err.forbidden":    "You do not have access to that.",
		"err.usernotfound": "User not found.",
		"in.num_invalid":   "That is not a valid number. Please send it again.",
		"in.toolong":       "That message is too long.",

		"sys.title":        "📊 <b>System status</b>",
		"sys.own_only":     "(your users only)",
		"sys.users":        "👥 Users: <b>%d</b>",
		"sys.by_status":    "🟢 active %d · 🔴 disabled %d · 🟠 data used up %d · ⌛ expired %d · ⏸ on hold %d",
		"sys.online":       "🟢 Online: <b>%d</b>",
		"sys.traffic":      "📈 Total traffic: ↑ %s · ↓ %s",
		"sys.speed":        "⚡ Speed: ↑ %s/s · ↓ %s/s",
		"sys.panel":        "🖥 Panel: CPU %s · RAM %s of %s",
		"sys.nodes":        "🌐 Nodes: <b>%d</b> of %d connected",
		"sys.tunnels_ok":   "🔐 WireGuard: all tunnels healthy",
		"sys.tunnels_bad":  "⚠️ WireGuard: %d tunnel(s) with a problem",
		"nodes.title":      "🖥 <b>Nodes</b>",
		"nodes.panel_line": "🖥 <b>Panel</b>",
		"nodes.none":       "No nodes are registered.",
		"nodes.status":     "Status: %s",
		"nodes.stale":      "⚠️ report is stale",
		"nodes.nometrics":  "no report received yet",
		"nodes.more":       "… and %d more node(s)",
		"wg.down":          "down",
		"wg.missing":       "missing",

		"users.menu":        "👥 <b>Users</b>\nPick a filter, or send part of a username to search.",
		"flt.all":           "All",
		"list.title":        "👥 <b>Users</b> · %s",
		"list.search_title": "🔎 <b>Search results</b> “%s”",
		"list.count":        "%d user(s) · page %d of %d",
		"list.empty":        "No users found.",

		"card.usage":         "📦 Used: %s / %s",
		"card.unlimited":     "unlimited",
		"card.expire":        "📅 Expires: %s (%s)",
		"card.expire_never":  "📅 Expires: never",
		"card.days_left":     "%d days left",
		"card.expired_ago":   "expired %d days ago",
		"card.onhold":        "⏸ Waiting for first connection (%d days once connected)",
		"card.online":        "🕐 Last seen: %s",
		"card.online_never":  "🕐 Last seen: never",
		"card.owner":         "🧑‍💼 Owner: %s",
		"card.owner_none":    "panel (no admin)",
		"card.note":          "📝 Note: %s",
		"card.synced":        "🔁 Managed by panel “%s”",
		"card.links":         "🔗 Subscription link:",
		"card.nolinks":       "🔗 No subscription link is available.",
		"card.nolinks_short": "No link available",
		"ago.now":            "just now",
		"ago.min":            "%d min ago",
		"ago.hour":           "%d h ago",
		"ago.day":            "%d d ago",

		"act.disable": "🔴 Disable",
		"act.enable":  "🟢 Enable",
		"act.reset":   "♻️ Reset usage",
		"act.extend":  "📅 Extend",
		"act.data":    "📦 Add data",
		"act.qr":      "📷 QR",
		"act.note":    "📝 Note",
		"act.revoke":  "🔗 Revoke link",
		"act.delete":  "🗑 Delete",

		"confirm.reset":  "♻️ Reset the data usage of %s?",
		"confirm.revoke": "🔗 Revoke the subscription link of %s and issue a new one? The current link and configs stop working.",
		"confirm.delete": "🗑 Permanently delete user %s?",

		"done.disabled":     "User disabled.",
		"done.enabled":      "User enabled.",
		"done.reset":        "Usage reset.",
		"done.revoked":      "Subscription link revoked; a new one was issued.",
		"done.deleted":      "Deleted.",
		"done.deleted_user": "🗑 User %s deleted.",
		"done.extended":     "Added %d days to the expiry.",
		"done.data":         "Added %s of data.",
		"done.note":         "Note saved.",
		"done.qr":           "QR code sent.",

		"ext.title":     "📅 <b>Extend %s</b>\nHow many days to add?",
		"ext.prompt":    "Send the number of days to add (1 to 3650).",
		"ext.unlimited": "This user has no expiry date, so there is nothing to extend.",
		"dat.title":     "📦 <b>Add data to %s</b>\nHow much?",
		"dat.prompt":    "Send the amount of data in GB, for example 5 or 2.5.",
		"dat.unlimited": "This user has unlimited data, so there is nothing to add.",
		"note.prompt":   "Send the new note (500 bytes at most). Send “-” to clear it.",
		"note.toolong":  "That note is longer than 500 bytes.",

		"nu.name":              "➕ <b>New user</b>\nSend the username (3 to 32 characters: letters, digits and _ ).",
		"nu.name_invalid":      "Invalid username: 3 to 32 characters of letters, digits and _ .",
		"nu.tmpl":              "➕ %s\nPick a template, or continue without one.",
		"nu.no_tmpl":           "No template (set manually)",
		"nu.limit":             "➕ %s\n📦 Pick the data limit in GB, or send a number (∞ = unlimited).",
		"nu.days":              "➕ %s\n📅 Pick the duration in days, or send a number (∞ = unlimited).",
		"nu.days_short":        "%d days",
		"nu.confirm":           "✅ <b>Confirm the new user</b>",
		"nu.summary_tmpl":      "🧩 Template: %s",
		"nu.summary_limit":     "📦 Data: %s",
		"nu.summary_life":      "📅 Duration: %s",
		"nu.summary_protocols": "🔌 Protocols: %s",
		"nu.create":            "✅ Create user",
		"nu.created":           "User created.",
		"nu.expired":           "That step expired. Start again from New user.",
		"nu.no_inbounds":       "No inbounds are configured on the panel yet.",
		"nu.change_name":       "✏️ Change username",
		"nu.usebuttons":        "Use the buttons for this step.",

		"bk.confirm":       "💾 Create a database backup and send it here?",
		"bk.create":        "💾 Create and send",
		"bk.working":       "⏳ Creating the backup…",
		"bk.working_short": "Working…",
		"bk.done":          "Backup sent.",
		"bk.toolarge":      "The backup (%s) is too large for Telegram; download it from the dashboard.",
		"bk.fail":          "Backup failed: %s",
		"bk.sendfail":      "The backup was created but could not be sent to Telegram.",
	},
}
