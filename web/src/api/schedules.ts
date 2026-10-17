// Types and fetchers for schedules (phase 108): list/create/delete on
// /api/executions/{execution_id}/schedules. Delegates to the generated
// client functions -- their paths, form encoding, and Schedule type match
// the real handler (schedule_handlers.go reads r.ParseForm, not JSON) --
// with two guards the raw generated surface lacks: undefined optional
// fields are omitted rather than stringified (the generated post runs
// String() over every Object.entries value, and a literal "undefined"
// fire_at would reach the server as a bogus RFC3339 instant and 400), and
// the list read normalizes Go's nil-slice null to [].
import {
  deleteExecutionsByExecutionIdSchedulesByScheduleId,
  getExecutionsByExecutionIdSchedules,
  postExecutionsByExecutionIdSchedules,
  type Occurrence,
  type Schedule,
} from './generated';

export type { Occurrence, Schedule };

/** The create body, exactly the fields createSchedule (the handler) reads
 * from the form: tenant_id -- the tenant whose quota the occurrences
 * reserve, resolved from the execution's project since no execution
 * response carries it -- plus kind and whichever of fire_at (RFC3339,
 * kind=one_shot) / recurrence (a standard 5-field cron expression,
 * kind=recurring) applies. There are no window fields: the server sizes
 * every occurrence's reservation from the execution's load profile. */
export interface CreateScheduleBody {
  tenantId: number;
  kind: 'one_shot' | 'recurring';
  fireAt?: string;
  recurrence?: string;
}

/** GET /api/executions/{id}/schedules -- the execution's schedules, each
 * with its occurrences and their statuses (reserved / rejected / fired /
 * completed). */
export async function listSchedules(executionId: number): Promise<Schedule[]> {
  const got = await getExecutionsByExecutionIdSchedules(executionId);
  return got ?? [];
}

/** POST /api/executions/{id}/schedules -- 201 with the created schedule,
 * including every occurrence computed within the 7-day admission horizon
 * and whether each was reserved or rejected by quota. */
export function createSchedule(executionId: number, body: CreateScheduleBody): Promise<Schedule> {
  const form: { tenant_id: number; kind: 'one_shot' | 'recurring'; fire_at?: string; recurrence?: string } = {
    tenant_id: body.tenantId,
    kind: body.kind,
  };
  if (body.fireAt !== undefined) {
    form.fire_at = body.fireAt;
  }
  if (body.recurrence !== undefined) {
    form.recurrence = body.recurrence;
  }
  return postExecutionsByExecutionIdSchedules(executionId, form);
}

/** DELETE /api/executions/{id}/schedules/{schedule_id} -- removes the
 * schedule and releases every occurrence that still holds a reservation. */
export function deleteSchedule(executionId: number, scheduleId: number): Promise<{ message?: string }> {
  return deleteExecutionsByExecutionIdSchedulesByScheduleId(executionId, scheduleId);
}
