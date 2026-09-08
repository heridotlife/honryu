import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import DashboardLayout from './components/DashboardLayout';
import { SessionProvider } from './hooks/useSession';
import Clusters from './pages/Clusters';
import Campaigns from './pages/Campaigns';
import Executions from './pages/Executions';
import NewTest from './pages/NewTest';
import Execution from './pages/Execution';
import ProfilePicker from './pages/ProfilePicker';
import Reports from './pages/Reports';
import Reservations from './pages/Reservations';
import RunCompare from './pages/RunCompare';
import SharedReport from './pages/SharedReport';

export default function App() {
  return (
    <BrowserRouter>
      {/* One /api/me fetch per app load feeds every consumer (picker, nav,
          action buttons) so the UI cannot drift from the server's answer. */}
      <SessionProvider>
        <DashboardLayout>
          <Routes>
            {/* Phase 20: / is the profile picker when unauthenticated and a
                redirect to /reports when a session cookie already exists. */}
            <Route path="/" element={<ProfilePicker />} />
            <Route path="/executions/new" element={<NewTest />} />
            <Route path="/executions" element={<Executions />} />
            <Route path="/executions/:id" element={<Execution />} />
            {/* Phase 21: run-over-run comparison for one execution (task 10). */}
            <Route path="/executions/:id/compare" element={<RunCompare />} />
            {/* R1: /status is now the execution hub's job; keep the bookmark alive. */}
            <Route path="/status" element={<Navigate to="/executions" replace />} />
            <Route path="/reports" element={<Reports />} />
            <Route path="/reports/:runId" element={<Reports />} />
            {/* Phase 34: the public share-out. Renders the run workspace
                read-only from the token alone; the picker never redirects
                an anonymous visitor away from it. */}
            <Route path="/share/:token" element={<SharedReport />} />
            <Route path="/reservations" element={<Reservations />} />

            <Route path="/campaigns" element={<Campaigns />} />
            <Route path="/clusters" element={<Clusters />} />
          </Routes>
        </DashboardLayout>
      </SessionProvider>
    </BrowserRouter>
  );
}
