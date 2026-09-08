// /share/:token — the public face of a share link (phase 34). Anyone
// holding the link reads one run's report read-only: no session, no
// redirect to the profile picker, ever (the token is the authorization).
// The report workspace is the same tree the session'd run detail renders,
// with the session-only affordances simply not wired (see ReportWorkspace).
import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import Button from '../components/ui/Button';
import { fetchShared } from '../api/reports';
import type { Report } from '../api/reports';
import { ReportWorkspace } from './Reports';

/**
 * Invalid-link states, deliberately one message for every cause (unknown,
 * revoked, expired): the page cannot tell a recipient which it was without
 * leaking that the token once existed, so it never tries.
 */
const INVALID_LINK_MESSAGE = 'This link is invalid or expired.';

export default function SharedReport() {
  const { token } = useParams<{ token: string }>();
  const [state, setState] = useState<{ kind: 'loading' } | { kind: 'ready'; report: Report } | { kind: 'invalid' }>({
    kind: 'loading',
  });

  useEffect(() => {
    // A route without a token cannot happen through the router, but a
    // mounted component answers honestly rather than fetching "/api/share/".
    if (!token) {
      setState({ kind: 'invalid' });
      return;
    }
    let cancelled = false;
    setState({ kind: 'loading' });
    fetchShared(token)
      .then(report => {
        if (!cancelled) setState({ kind: 'ready', report });
      })
      .catch(() => {
        if (!cancelled) setState({ kind: 'invalid' });
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  if (state.kind === 'loading') {
    return (
      <div className="space-y-6" data-testid="shared-loading">
        <Card>
          <CardContent>
            <p className="text-body-sm text-slate-500 dark:text-slate-400">Loading shared report…</p>
          </CardContent>
        </Card>
      </div>
    );
  }

  if (state.kind === 'invalid') {
    return (
      <div className="space-y-6" data-testid="shared-invalid">
        <Card>
          <CardHeader>
            <CardTitle>Shared report</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <p className="text-body-sm text-slate-600 dark:text-slate-300" role="alert">
              {INVALID_LINK_MESSAGE}
            </p>
            <p className="text-caption text-slate-500 dark:text-slate-400">
              Ask whoever shared it for a fresh link — links can expire or be revoked.
            </p>
            {/* "/" is the profile picker for the anonymous visitor: the way
                in for operators, harmless for everyone else. */}
            <Link to="/">
              <Button variant="secondary" size="sm" data-testid="shared-home-btn">
                Go to the start page
              </Button>
            </Link>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-6" data-testid="shared-report">
      {/* Read-only, for everyone the same: no share button, no prev/next
          run jumps, no siblings (so no compare card) — just the report. */}
      <ReportWorkspace report={state.report} siblings={null} />
    </div>
  );
}
