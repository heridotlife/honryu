import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import DashboardLayout from './components/DashboardLayout';
import { SessionProvider } from './hooks/useSession';
import Clusters from './pages/Clusters';
import Campaigns from './pages/Campaigns';
import NewTest from './pages/NewTest';
import Execution from './pages/Execution';
import Home from './pages/Home';
import ProfilePicker from './pages/ProfilePicker';
import Reports from './pages/Reports';
import Reservations from './pages/Reservations';
import RunCompare from './pages/RunCompare';
import Scenario from './pages/Scenario';
import Scenarios from './pages/Scenarios';
import SharedReport from './pages/SharedReport';
import Tenants from './pages/Tenants';

export default function App() {
  return (
    <BrowserRouter>
      {/* One /api/me fetch per app load feeds every consumer (picker, nav,
          action buttons) so the UI cannot drift from the server's answer. */}
      <SessionProvider>
        <DashboardLayout>
          <Routes>
            {/* Phase 20: / is the profile picker when unauthenticated and a
                redirect to the dashboard when a session cookie already
                exists. Phase 52: that dashboard is /home, not the bare run
                list. */}
            <Route path="/" element={<ProfilePicker />} />
            <Route path="/home" element={<Home />} />
            <Route path="/executions/new" element={<NewTest />} />
            {/* Phase 67b: the scenario-first inversion completes. The flat
                run list is no longer a surface of its own -- the URL
                redirects so old links and bookmarks land on /scenarios,
                where the run history lives now. The page component stays in
                the tree (pages/Executions.tsx, still under test); the run
                hub below keeps its URL -- RunCompare and Reports link
                there. */}
            <Route path="/executions" element={<Navigate to="/scenarios" replace />} />
            <Route path="/executions/:id" element={<Execution />} />
            {/* Phase 67b: the scenario-first inversion -- /scenarios is the
                primary run surface (list here, tabbed detail at :id). The
                run hub stays at /executions/:id. */}
            <Route path="/scenarios" element={<Scenarios />} />
            {/* Phase 65: where a template instantiation lands (there is no
                scenario list -- the NewTest picker browses the catalog). */}
            <Route path="/scenarios/:id" element={<Scenario />} />
            {/* Phase 21: run-over-run comparison for one execution (task 10). */}
            <Route path="/executions/:id/compare" element={<RunCompare />} />
            {/* R1: /status is now the scenario list's job; keep the
                bookmark alive, one hop (not chained through /executions,
                which redirects too since phase 67b). */}
            <Route path="/status" element={<Navigate to="/scenarios" replace />} />
            <Route path="/reports" element={<Reports />} />
            <Route path="/reports/:runId" element={<Reports />} />
            {/* Phase 34: the public share-out. Renders the run workspace
                read-only from the token alone; the picker never redirects
                an anonymous visitor away from it. */}
            <Route path="/share/:token" element={<SharedReport />} />
            <Route path="/reservations" element={<Reservations />} />

            <Route path="/campaigns" element={<Campaigns />} />
            {/* Phase 35: the tenant admin console (quota, members, create). */}
            <Route path="/tenants" element={<Tenants />} />
            <Route path="/clusters" element={<Clusters />} />
          </Routes>
        </DashboardLayout>
      </SessionProvider>
    </BrowserRouter>
  );
}
