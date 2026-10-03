export const APPROVAL_STORAGE_KEY = 'aegis-agent.webconsole.approval-operations.v1';

function copy(value) {
  return JSON.parse(JSON.stringify(value));
}

function sameTarget(a, b) {
  return a?.plan_mode_id === b?.plan_mode_id && a?.plan_version === b?.plan_version &&
    a?.expected_revision === b?.expected_revision;
}

// This stores pending user intent. Receipts and current run facts always come
// from the server; a saved phase never authorizes execution after a reload.
export class ApprovalOperationController {
  constructor({ storage, requestID, submit, query, onChange = () => {} }) {
    this.storage = storage;
    this.requestID = requestID;
    this.submit = submit;
    this.query = query;
    this.onChange = onChange;
    this.inFlight = new Map();
    this.responses = new Map();
    this.checked = new Set();
  }

  read() {
    const raw = this.storage.getItem(APPROVAL_STORAGE_KEY);
    if (!raw) return { version: 1, sessions: {} };
    let saved;
    try { saved = JSON.parse(raw); } catch { throw new Error('Saved approval could not be read. Check the session before approving again.'); }
    if (saved?.version !== 1 || !saved.sessions || typeof saved.sessions !== 'object' || Array.isArray(saved.sessions)) {
      throw new Error('Saved approval could not be read. Check the session before approving again.');
    }
    return saved;
  }

  get(sessionID) {
    const record = this.read().sessions[sessionID];
    if (!record) return null;
    if (record.session_id !== sessionID || !record.approval_request_id ||
      !['planmode', 'mission'].includes(record.entrypoint) || !record.parameters?.plan_mode_id ||
      !record.parameters.expected_revision || !Number.isInteger(record.parameters.plan_version) ||
      record.parameters.plan_version < 1 || typeof record.parameters.override_coverage !== 'boolean') {
      throw new Error('Saved approval could not be read. Check the session before approving again.');
    }
    return copy(record);
  }

  save(record, expectedID = '') {
    const saved = this.read();
    if (expectedID && saved.sessions[record.session_id]?.approval_request_id !== expectedID) return false;
    saved.sessions[record.session_id] = { ...copy(record), updated_at: new Date().toISOString() };
    try { this.storage.setItem(APPROVAL_STORAGE_KEY, JSON.stringify(saved)); }
    catch { throw new Error('Approval could not be saved. Enable local storage before approving.'); }
    this.onChange(record.session_id);
    return true;
  }

  busy(sessionID) { return this.inFlight.has(sessionID); }
  response(sessionID) {
    const response = this.responses.get(sessionID);
    return response?.approval?.lookup?.binding?.approval_request_id === this.get(sessionID)?.approval_request_id ? response : null;
  }

  run(sessionID, action) {
    if (this.inFlight.has(sessionID)) return this.inFlight.get(sessionID);
    // Register before invoking an action: onChange can rerender other Approve
    // controls, and every alias in this page must share the same pending ID.
    let resolve, reject;
    const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
    this.inFlight.set(sessionID, promise);
    try { Promise.resolve(action()).then(resolve, reject); } catch (err) { reject(err); }
    promise.finally(() => {
      if (this.inFlight.get(sessionID) === promise) this.inFlight.delete(sessionID);
      this.onChange(sessionID);
    }).catch(() => {});
    return promise;
  }

  start(sessionID, entrypoint, parameters) {
    return this.run(sessionID, async () => {
      const old = this.get(sessionID);
      if (old && (!['admitted', 'failed', 'rejected'].includes(old.phase) ||
        (old.phase === 'rejected' && sameTarget(old.parameters, parameters)))) {
        return this.checkRecord(old);
      }
      const record = { session_id: sessionID, approval_request_id: this.requestID(), entrypoint,
        parameters: { ...copy(parameters), override_coverage: Boolean(parameters.override_coverage) }, phase: 'pending' };
      this.save(record);
      return this.post(record);
    });
  }

  check(sessionID) {
    return this.run(sessionID, () => {
      const record = this.get(sessionID);
      if (!record) throw new Error('No saved approval is available.');
      return this.checkRecord(record);
    });
  }

  restore(sessionID) {
    const record = this.get(sessionID);
    if (!record || this.busy(sessionID) || this.checked.has(record.approval_request_id)) return null;
    this.checked.add(record.approval_request_id);
    return this.check(sessionID);
  }

  retry(sessionID) {
    return this.run(sessionID, () => {
      const record = this.get(sessionID);
      if (!record || !['not_found', 'prepared', 'recovery_required'].includes(record.phase)) {
        throw new Error('Check the saved approval before retrying.');
      }
      return this.post(record);
    });
  }

  override(sessionID, parameters) {
    return this.run(sessionID, () => {
      const old = this.get(sessionID);
      if (!old || old.phase !== 'rejected' || !sameTarget(old.parameters, parameters)) {
        throw new Error('The plan has changed. Please review it again.');
      }
      const record = { ...old, approval_request_id: this.requestID(),
        parameters: { ...copy(old.parameters), override_coverage: true }, phase: 'pending', notice: '' };
      this.save(record, old.approval_request_id);
      return this.post(record);
    });
  }

  observe(record, response) {
    const lookup = response?.approval?.lookup;
    const stage = lookup?.receipt?.stage;
    if (!lookup?.found || lookup.binding?.approval_request_id !== record.approval_request_id ||
      !['prepared', 'admitted', 'rejected'].includes(stage)) {
      throw new Error('Approval response could not be verified. Check approval before retrying.');
    }
    const phase = response.approval.recovery_required ? 'recovery_required' : stage;
    if (this.save({ ...record, phase, notice: '' }, record.approval_request_id)) {
      this.responses.set(record.session_id, response);
      this.checked.add(record.approval_request_id);
      return response;
    }
    return null;
  }

  async checkRecord(record) {
    try {
      return this.observe(record, await this.query(record.session_id, record.approval_request_id));
    } catch (err) {
      if (err.status === 404) {
        this.save({ ...record, phase: record.phase === 'recovery_required' ? 'recovery_required' : 'not_found' }, record.approval_request_id);
      } else if (err.code === 'APPROVAL_RECOVERY_REQUIRED') {
        this.save({ ...record, phase: 'recovery_required', notice: err.message }, record.approval_request_id);
      } else {
        this.save({ ...record, phase: 'unknown' }, record.approval_request_id);
      }
      this.responses.delete(record.session_id);
      return null;
    }
  }

  async post(record) {
    this.save({ ...record, phase: 'pending' }, record.approval_request_id);
    try {
      return this.observe(record, await this.submit(record.session_id, record.entrypoint,
        { ...copy(record.parameters), approval_request_id: record.approval_request_id }));
    } catch (err) {
      if (err.payload?.approval?.lookup?.found) return this.observe(record, err.payload);
      if (err.code === 'APPROVAL_RECOVERY_REQUIRED') {
        this.save({ ...record, phase: 'recovery_required', notice: err.message }, record.approval_request_id);
        return null;
      }
      // A structured, definitive rejection has no admitted receipt. Transport
      // failures (including unreadable success bodies) must be queried first.
      if (err.status >= 400 && err.status < 500) {
        this.save({ ...record, phase: 'failed', notice: err.message }, record.approval_request_id);
        throw err;
      }
      this.save({ ...record, phase: 'unknown' }, record.approval_request_id);
      return this.checkRecord(record);
    }
  }
}
