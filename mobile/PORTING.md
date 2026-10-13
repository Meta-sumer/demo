# Porting the web client to React Native

`client/` (React + Vite + Tailwind, ~27k lines) is being ported, feature for
feature, to `mobile/` (Expo SDK 54, React Native 0.81, expo-router 6,
NativeWind 4). This file is the contract every port follows.

**Source of truth:** `client/src/**`. Read each web file in full before
porting it. Keep its behaviour, its states (loading / empty / error / disabled),
its copy text word for word, its query keys, and its doc comments (adapt any
sentence that only made sense on the web). Do not invent features, and do not
drop any - if something truly cannot exist on a phone, say so in your report.

## What already exists in `mobile/src` (do not rewrite)

| Area | Files |
|---|---|
| API + auth | `services/api.ts` (same `api` object; deposit `proof` is an `UploadFile {uri,name,type}`), `services/http.ts` (`authenticatedSource(path)` for authed images), `config.ts` |
| Realtime | `websocket/client.ts`, `hooks/useRealtime.ts` (mounted in the shell) |
| State | every store in `stores/` (persisted ones use AsyncStorage), `features/trade/ticketStore.ts` |
| Pure logic (unchanged from web) | `types/api.ts`, `utils/*` (format, time, indicators, usdt, csv → `shareCsv`, dayGroups, textSearch, countries, tradeStatus, redraw), `components/assetList.ts`, `features/trade/{ticketModel,settlement,liveResult,drawRule,announceResult,useActiveTrades,useConnectionState,useTradingWallet,useLiveFeeds}.ts`, `features/trade/chart/{drawingGeometry,indicatorCatalog,tradeWindow}.ts`, `features/trade/orders/orderModel.ts`, `features/usdt/{model,queries}.ts`, `features/account/funds.ts`, `features/auth/schemas.ts` |
| Hooks | `hooks/useCountdown.ts` (`useCountdown`, `useNow`), `useDebouncedValue.ts`, `useToast.ts` (`toast(tone,title,desc)`) |
| Theme | `theme/tokens.ts` (3 themes, same values as `client/src/index.css`), `utils/theme-colors.ts` (`useChartPalette()`, `withAlpha()`, `useTokenColor(name)`) |
| UI kit | `components/ui` - `Text`, `Icon`, `Button`, `IconButton`, `Panel`, `Stat`, `Badge`, `FeedBadge`, `Skeleton`, `Spinner`, `EmptyState`, `Divider`, `Label`, `Field`, `Sheet`, `Modal`, `Switch`, `ToggleRow`, `Segmented`, `Select`, `ListRow`, `Toaster` |
| Shell | `app/_layout.tsx` (providers, auth gate), `app/(app)/_layout.tsx` (realtime + overlays), `app/(app)/(tabs)/_layout.tsx`, `components/{TopBar,BottomNav,AccountSwitch,ThemeToggle,Screen,InfoTip,AssetRow}.tsx`, `features/trade/tradeBits.tsx` |

Read `components/ui/*.tsx` and `components/Screen.tsx` before you start - they
are small, and using them keeps every screen consistent.

## Element mapping

| Web | React Native |
|---|---|
| `div`, `section`, `ul/li`, `header`, `nav`, `aside` | `View` |
| any text (`span`, `p`, `h1-h6`, `label`, `dt/dd`) | `Text` **from `components/ui`** - never RN's `Text` directly. Every string must be inside a `Text`. |
| `button` | `Pressable` (use `active:` classes for pressed state) or `Button` / `IconButton` |
| `input` / `textarea` | `Field` (or `TextInput` with the same classes); numeric → `keyboardType="decimal-pad"` |
| `select` | `Select` (a field that opens a sheet) or `Segmented` for 2-4 options |
| checkbox / toggle | `Switch` / `ToggleRow` |
| `Link` / `useNavigate` (react-router) | `Link` / `router` from `expo-router` (see routes below) |
| `useSearchParams` | `useLocalSearchParams` + `router.setParams` |
| lucide-react `<X className="h-4 w-4 text-x" />` | `<Icon as={X} size={16} className="text-x" />` (lucide-react-native) |
| `Modal`, `Sheet` (web) | `Modal`, `Sheet` from `components/ui` (bottom sheets) |
| popover / dropdown menu | `Sheet` |
| `navigator.clipboard.writeText` | `Clipboard.setStringAsync` from `expo-clipboard` |
| `window.open(url)` / external `<a>` | `WebBrowser.openBrowserAsync(url)` from `expo-web-browser` |
| `<input type="file">` (images) | `ImagePicker.launchImageLibraryAsync` from `expo-image-picker` → `UploadFile` |
| `downloadCsv` | `shareCsv(filename, csv)` from `utils/csv` |
| `<img src>` | `Image` from `expo-image` (`source={authenticatedSource(path)}` for authed URLs) |
| SVG art | `react-native-svg` |
| CSS keyframe animations | `react-native-reanimated` (`entering={FadeIn}`, shared values) |
| `localStorage` | a persisted zustand store (`stores/storage.ts`) or AsyncStorage |
| `crypto.randomUUID()` | `randomUUID` from `expo-crypto` |
| `window.setTimeout` etc. | `setTimeout` etc. (types: `ReturnType<typeof setTimeout>`) |
| `useMediaQuery` / breakpoints | phone layout only - port the **mobile (< 640px)** variant of every responsive class |
| `aria-label` / `sr-only` | `accessibilityLabel` / `accessibilityRole` / `accessibilityState` |
| haptic moments (trade placed, result) | optional `Haptics` from `expo-haptics` |

## NativeWind rules (Tailwind on RN)

- Text does **not** inherit from its parent. Colour, size and weight go on each `Text`.
  `Text` defaults to `text-sm text-content-100`; your classes override via `cn()`.
- Not supported - do not use: `grid`/`grid-cols-*` (use `flex-row flex-wrap` + widths like
  `w-1/2`, or `flex-1` children), `hover:`, `focus-visible:`, `group-*`, `peer-*`,
  responsive prefixes (`sm: md: lg: xl:`), `space-x/y-*` (use `gap-*`), `truncate`
  (use `numberOfLines={1}`), `animate-*`, `transition*`, `backdrop-blur*`, `ring-*`
  (use `border`), `shadow-panel/raised/overlay`, `cursor-*`, `select-none`, `sr-only`,
  `inline-flex`/`inline-block` (just `flex-row` / `self-start`), `place-items-*` (use
  `items-center justify-center`), `divide-*` (add borders per row), `tracking-*` is fine.
- `tnum` in a `Text` className turns on tabular figures (handled by `Text`).
- Colours are the same tokens as the web: `bg-surface-900`, `text-content-400`,
  `border-surface-700`, `bg-up/15`, `text-down`, `bg-accent-solid`, `text-warn`, `bg-brand`...
- Merge classes with `cn()` from `utils/cn` (clsx + tailwind-merge), not bare `clsx`.
- Colour strings for non-class props (icons' `fill`, `ActivityIndicator`, Skia, SVG):
  `useTokenColor('accent')` or `useChartPalette()`.
- `View` defaults to `flex-col`; `flex-1` children need a parent with a size.

## Screens and routes

| Web route | Mobile route file (`src/app/...`) | Notes |
|---|---|---|
| `/` (Landing) | `welcome.tsx` | public only |
| `/login`, `/register` | `login.tsx`, `register.tsx` | public only; the gate redirects on sign-in. After register, `router.replace('/onboarding')` if onboarding not completed |
| `/terms`, `/privacy` | `terms.tsx`, `privacy.tsx` | readable signed in or out; use `ScreenHeader` |
| `/onboarding` | `onboarding.tsx` | full screen |
| `/home`, `/markets`, `/trade`, `/trades`, `/account` | `(app)/(tabs)/<name>.tsx` | tabs; TopBar and BottomNav come from the tabs layout - do not render them; no `ScreenHeader` |
| `/trades/:id` | `(app)/trades/[id].tsx` | pushed; `ScreenHeader` + `Screen bottomInset` |
| `/learn`, `/settings`, `/deposit`, `/withdraw` | `(app)/<name>.tsx` | pushed |
| `/account/support` | `(app)/support.tsx` → href `/support` | pushed |
| `/wallet`, `/wallet/usdt`, `/wallet/usdt/success` | `(app)/wallet/index.tsx`, `(app)/wallet/usdt/index.tsx`, `(app)/wallet/usdt/success.tsx` | pushed |
| `/account#notifications` etc. | `/account?section=notifications` | the Account tab scrolls to `section` |

A route file should stay thin: put the screen in a component (e.g.
`features/<area>/<Name>Screen.tsx` or `screens/<Name>.tsx`) and have the route
file render it, so a screen can also be reused (e.g. inside a sheet).

Tab screens: wrap content in `<Screen>` (scroll + padding). Pushed screens:
`<View className="flex-1 bg-surface-950"><ScreenHeader title=.. /><Screen bottomInset>...</Screen></View>`.
Use `FlatList` for long or paginated lists instead of mapping inside a ScrollView.

Overlays already mounted for the whole session (do not mount again):
`TransferModal` (open with `openFunds('DEPOSIT'|'WITHDRAWAL')`), `SettingsDrawer`
(open with `useWorkspaceStore.getState().setSettingsOpen(true)`), `TradeResultToasts`, `Toaster`.

## Ownership

Each port owns a set of files (listed in its brief). Write only those, plus new
files under your own area. If you need a change in a shared file (ui kit,
stores, services, utils, shell), keep it minimal and additive (a new optional
prop, a new export) and list it in your report. Some shared components exist
as **stubs** with a fixed signature (PriceChart, TradeWindowChart,
AssetBrowser, SettingsDrawer, usdt/parts, TransferModal, TradeResultToasts);
the owner replaces the stub and must keep that signature.

## Checks before you report

1. `npx tsc --noEmit` from `mobile/` - fix every error in **your** files. Other
   ports are running at the same time; ignore errors in files you do not own.
2. Do not run `expo start`, `expo export` or `npm install` (ask in your report
   if you need a package).
3. Report: files written, anything you could not port and why, shared-file
   changes, and anything the integrator must wire up.
