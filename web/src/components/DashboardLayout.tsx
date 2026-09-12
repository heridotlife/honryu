import { useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { LogOut, Menu, Moon, Plus, Sun, X } from 'lucide-react';
import Button from './ui/Button';
import ProjectSwitcher from './ProjectSwitcher';
import { useSession } from '../hooks/useSession';

export interface NavItem {
  href: string;
  label: string;
  /** The permission whose absence removes this item from the nav. */
  resource: string;
  action: string;
}

/** Every nav surface, in order; visibility is the session's call. */
const allNavItems: NavItem[] = [
  // report:read -- Home (phase 52) reads the same reports and executions
  // the Reports page reads, so it rides the same grant and lands the
  // authenticated session somewhere that answers "what is happening".
  { href: '/home', label: 'Home', resource: 'report', action: 'read' },
  // report:read -- the Reports page's primary fetches are report reads.
  { href: '/reports', label: 'Reports', resource: 'report', action: 'read' },
  // execution:list -- GET /api/executions is the caller-scoped list.
  { href: '/executions', label: 'Executions', resource: 'execution', action: 'list' },
  // schedule:list -- reservations ARE schedules (phase 20's resource fold).
  { href: '/reservations', label: 'Reservations', resource: 'schedule', action: 'list' },
  // campaign:list -- only campaign_manager and the admin hold any campaign grant.
  { href: '/campaigns', label: 'Campaigns', resource: 'campaign', action: 'list' },
  // tenant:admin -- the Tenants console (phase 35): the service provider's
  // admin (wildcard) and each tenant's own admins (session.ts's
  // canAdminTenants mirrors this pair exactly).
  { href: '/tenants', label: 'Tenants', resource: 'tenant', action: 'admin' },
  // system:admin -- clusters are the service provider's fleet (AC4).
  { href: '/clusters', label: 'Clusters', resource: 'system', action: 'admin' },
];

/**
 * navItems as a function of the session's permission map (phase 20): each
 * entry survives only if the server said this caller may use that surface.
 * Unauthenticated sessions hold nothing, so the nav renders just the logo
 * until a persona is picked. The resource:action pairs mirror the HTTP
 * audit table (authz_audit_test.go), so nav and routes cannot drift.
 */
export function navItemsFor(can: (resource: string, action: string) => boolean): NavItem[] {
  return allNavItems.filter((item) => can(item.resource, item.action));
}

function applyTheme(dark: boolean) {
  document.documentElement.classList.toggle('dark', dark);
}

/**
 * Classes for the mobile nav drawer, exported so the layout invariant is
 * testable without a layout engine (jsdom computes no geometry).
 *
 * The invariant: the drawer is `absolute` in BOTH states. A closed drawer
 * that participates in normal flow still reserves its full height even when
 * `invisible` -- that reserved band pushed <main> down ~306px on every page
 * (nav measured 370px against h-16's 64px), which read on a phone as a
 * screenful of empty space above the content. Positioning it out of flow is
 * what fixes that, so `absolute` is the thing worth pinning down.
 */
export function mobileMenuClasses(isOpen: boolean): string {
  const base =
    'mobile-menu absolute top-full right-0 left-0 transform border-b border-slate-200 bg-white/95 shadow-lg backdrop-blur-md transition-all duration-300 ease-in-out dark:border-slate-800 dark:bg-slate-950/95 md:hidden';
  // pointer-events-none/invisible: while closed the drawer still overlaps the
  // nav bar, so without these its links swallow taps meant for the burger
  // button. -translate-y-full (not -2) slides it fully behind the bar.
  const state = isOpen ? 'translate-y-0 opacity-100' : 'pointer-events-none invisible -translate-y-full opacity-0';
  return `${base} ${state}`;
}

/**
 * The house focus ring (ui/Button's), reused for the raw <a> nav links so
 * every focusable element in the shell shows the same keyboard affordance.
 * focus: (not focus-visible:) matches Button's established contract.
 */
const focusRing = 'focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500';

/**
 * Classes for the skip-to-content link: screen-reader-only until focused,
 * then pinned over the nav (z-50 beats the nav's z-40) instead of reflowing
 * the page underneath. Tailwind's documented skip-link pattern
 * (sr-only + focus:not-sr-only) plus positioning.
 */
const skipLinkClasses =
  'sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-2 focus:z-50 focus:rounded-md focus:bg-sky-600 focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:text-white focus:shadow-lg';

export default function DashboardLayout({ children }: { children: ReactNode }) {
  const location = useLocation();
  const navigate = useNavigate();
  const { session, can, logout } = useSession();
  const navItems = navItemsFor(can);
  const [isDark, setIsDark] = useState(false);
  const [mounted, setMounted] = useState(false);
  const [isMobileMenuOpen, setIsMobileMenuOpen] = useState(false);

  useEffect(() => {
    const savedTheme = localStorage.getItem('honryu-theme');
    const shouldBeDark = savedTheme ? savedTheme === 'dark' : window.matchMedia('(prefers-color-scheme: dark)').matches;
    setIsDark(shouldBeDark);
    applyTheme(shouldBeDark);
    setMounted(true);
  }, []);

  const toggleTheme = () => {
    const next = !isDark;
    setIsDark(next);
    localStorage.setItem('honryu-theme', next ? 'dark' : 'light');
    applyTheme(next);
  };

  // Logout expires the cookie, re-resolves identity (the hook's refresh),
  // and sends the SPA back to the picker (AC13).
  const handleLogout = async () => {
    await logout();
    navigate('/');
  };

  // Tapping anywhere outside closes the open drawer. Without this the only
  // way out is the X, since the drawer overlays the page rather than pushing
  // it down -- tapping the content you can see underneath would otherwise do
  // nothing. Matched on marker classes rather than refs so the burger button
  // itself is excluded: it owns its own toggle, and letting this handler see
  // it too would close-then-reopen on a single tap.
  useEffect(() => {
    if (!isMobileMenuOpen) {
      return;
    }
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Element | null;
      if (!target?.closest('.mobile-menu') && !target?.closest('.mobile-menu-button')) {
        setIsMobileMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [isMobileMenuOpen]);

  // Escape closes the open drawer -- keyboard parity with the tap-away close
  // above. Same guard: no listener while the drawer is already closed.
  useEffect(() => {
    if (!isMobileMenuOpen) {
      return;
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setIsMobileMenuOpen(false);
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [isMobileMenuOpen]);

  if (!mounted) {
    return null;
  }

  return (
    <div className="min-h-screen bg-white text-slate-700 transition-colors duration-300 dark:bg-slate-950 dark:text-slate-300">
      {/* Keyboard entry point (phase 23): first focusable in the DOM, so the
          first Tab lands here and jumps straight to the content instead of
          walking the whole nav. sr-only until focused (skipLinkClasses). */}
      <a href="#main" data-testid="skip-link" className={skipLinkClasses}>
        Skip to content
      </a>
      <nav className="sticky top-0 z-40 border-b border-slate-200/70 bg-white/80 backdrop-blur-md dark:border-slate-800/70 dark:bg-slate-950/80">
        <div className="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
          <div className="flex h-16 items-center justify-between">
            <div className="flex items-center space-x-8">
              <Link
                to="/"
                className={`text-xl font-bold tracking-tight text-slate-900 dark:text-white ${focusRing}`}
              >
                Honryu<span className="text-sky-500">.</span>
              </Link>
              <div data-testid="nav-links" className="hidden space-x-2 md:flex">
                {navItems.map((item) => (
                  <Link
                    key={item.href}
                    to={item.href}
                    className={`flex min-h-[44px] items-center rounded-md px-3 py-2 text-sm font-medium transition-colors duration-200 ${focusRing} ${
                      location.pathname === item.href
                        ? 'bg-sky-50 text-sky-700 dark:bg-sky-900/30 dark:text-sky-300'
                        : 'text-slate-600 hover:bg-slate-100 hover:text-sky-600 dark:text-slate-300 dark:hover:bg-slate-800 dark:hover:text-sky-400'
                    }`}
                  >
                    {item.label}
                  </Link>
                ))}
              </div>
            </div>

            {/* One right-side control group for every viewport (phase 32):
                the New Test CTA leads (phase 52: the one amber accent per
                view -- the audit's "no visible primary action" finding),
                then the project switcher, then the theme toggle, then the
                burger (mobile only). A single switcher/theme instance keeps
                the nav to one /api/projects fetch per page. */}
            <div className="flex items-center space-x-2 md:space-x-4">
              {can('execution', 'create') && (
                <Button
                  variant="accent"
                  size="md"
                  data-testid="nav-new-test"
                  aria-label="New Test"
                  title="Start a new test"
                  onClick={() => navigate('/executions/new')}
                >
                  <Plus className="h-5 w-5 md:mr-1 md:h-4 md:w-4" aria-hidden />
                  {/* Full label from md up; icon-only below it -- a 375px
                      phone cannot fit label + switcher + toggles in one
                      bar, and the row must never push the viewport sideways
                      (the exact bug phase 52's e2e F-check pins). */}
                  <span className="hidden md:inline">New Test</span>
                </Button>
              )}
              <ProjectSwitcher />
              <Button
                onClick={toggleTheme}
                variant="ghost"
                size="md"
                className="min-h-[44px] min-w-[44px] p-2 text-amber-500 dark:text-sky-600"
                title="Toggle theme"
              >
                {isDark ? <Sun className="h-5 w-5" /> : <Moon className="h-5 w-5" />}
              </Button>
              <Button
                onClick={() => setIsMobileMenuOpen((open) => !open)}
                variant="ghost"
                size="md"
                className="mobile-menu-button min-h-[44px] min-w-[44px] p-2 text-slate-600 dark:text-slate-300 md:hidden"
                title="Toggle menu"
              >
                {isMobileMenuOpen ? <X className="h-5 w-5" /> : <Menu className="h-5 w-5" />}
              </Button>
            </div>
          </div>
        </div>

        <div className={mobileMenuClasses(isMobileMenuOpen)}>
          <div className="space-y-2 px-4 py-4">
            {navItems.map((item) => (
              <Link
                key={item.href}
                to={item.href}
                onClick={() => setIsMobileMenuOpen(false)}
                className={`block min-h-[44px] rounded-lg px-4 py-3 text-base font-medium transition-colors duration-200 ${focusRing} ${
                  location.pathname === item.href
                    ? 'bg-sky-50 text-sky-700 dark:bg-sky-900/30 dark:text-sky-300'
                    : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'
                }`}
              >
                {item.label}
              </Link>
            ))}
          </div>
        </div>
      </nav>

      <main id="main" className="relative z-10 mx-auto max-w-7xl px-4 py-4 sm:px-6 sm:py-6 lg:px-8 lg:py-8">
        {/* Persistent demo banner, INSIDE main so the nav-height and
            main-under-nav layout invariants are unaffected. Demo mode is a
            credential-free front door -- the banner must never go away. */}
        {session?.demo === true && (
          <div
            data-testid="demo-banner"
            role="status"
            className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 dark:border-amber-700 dark:bg-amber-900/30"
          >
            <p className="text-sm text-amber-900 dark:text-amber-200">
              Demo mode — signed in as <strong>{session.name}</strong>. Selecting a persona is the authentication; anyone
              who can reach this page can select any profile.
            </p>
            <Button
              variant="outline"
              size="sm"
              onClick={() => void handleLogout()}
              className="min-h-[44px] border-amber-400 text-amber-900 hover:bg-amber-100 dark:border-amber-600 dark:text-amber-200 dark:hover:bg-amber-900/50"
            >
              <LogOut className="mr-1 h-4 w-4" aria-hidden />
              Log out
            </Button>
          </div>
        )}
        {children}
      </main>
    </div>
  );
}
