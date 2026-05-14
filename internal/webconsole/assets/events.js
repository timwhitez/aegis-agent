function describeTimelineItem(item) {
  if (item.kind === 'message') {
    if (isBackgroundResultsMessage({ text: item.text, meta: item.data || {} })) {
      const payload = parseBackgroundResultsPayload(item.text);
      const results = maybeArray(payload?.background_results);
      const failed = results.filter((result) => backgroundResultTone(result) === 'danger').length;
      return {
        icon: 'git-branch',
        title: 'Background results accepted',
        copy: summarizeBackgroundResultsPayload(payload) || 'Background agent results were accepted into this session.',
        meta: item.message_id ? shortId(item.message_id) : '',
        tone: failed ? 'danger' : 'live',
        data: ''
      };
    }
    if (item.role === 'tool') {
      return {
        icon: 'wrench',
        title: 'Tool results appended',
        copy: truncateText(item.text || 'Tool output recorded.', 180),
        meta: item.message_id ? shortId(item.message_id) : '',
        tone: 'live',
        data: ''
      };
    }
    return {
      icon: item.role === 'user' ? 'user' : item.role === 'system' ? 'terminal' : 'message-square',
      title: `${humanizeStatus(item.role || 'message')} message`,
      copy: truncateText(item.text || '(empty message)', 180),
      meta: item.message_id ? shortId(item.message_id) : '',
      tone: item.role === 'system' ? 'danger' : 'neutral',
      data: ''
    };
  }

  return describeEventDescriptor(item.event_type, item.data, item.phase, item.event_id);
}

function isCompactFlowEvent(eventType) {
  return [
    'provider.call',
    'tool.before',
    'tool.after',
    'tool.interrupted',
    'tool.blocked',
    'session.child.spawned',
    'session.child.queued',
    'queue.job.claimed',
    'queue.job.completed',
    'queue.job.failed',
    'goal.created',
    'goal.updated',
    'goal.budget_limited',
    'goal.budget_wrapup_required',
    'goal.progress.recorded',
    'goal.completed',
    'planmode.created',
    'planmode.input_requested',
    'planmode.input_answered',
    'planmode.input_cancelled',
    'planmode.plan_submitted',
    'planmode.plan_approved',
    'planmode.plan_revised',
    'planmode.cancelled',
    'planmode.execution_started',
    'mission.plan.updated',
    'mission.plan.approved',
    'mission.validation.updated',
    'provider.cancelled',
    'session.paused',
    'session.completed',
    'session.failed'
  ].includes(eventType || '');
}

function describeEventDescriptor(eventType, data, phase, eventID) {
  const toolName = data?.tool_name || data?.tool || '';
  switch (eventType) {
    case 'provider.call':
      return {
        icon: 'radio',
        title: 'Provider call',
        copy: 'Waiting for the model provider.',
        meta: phaseHeadline(phase),
        tone: 'live',
        data: ''
      };
    case 'provider.request.prepared':
      return {
        icon: 'package-search',
        title: 'Request prepared',
        copy: 'Durable turn request assembled.',
        meta: phaseHeadline(phase),
        tone: 'neutral',
        data: ''
      };
    case 'assistant.message':
      return {
        icon: 'sparkles',
        title: 'Assistant output persisted',
        copy: truncateText(data?.text || 'Assistant text recorded.', 180),
        meta: phaseHeadline(phase),
        tone: 'live',
        data: ''
      };
    case 'tool.before':
      return {
        icon: 'wrench',
        title: `Tool started: ${toolName || 'tool'}`,
        copy: summarizeToolArgumentsData(data?.arguments),
        meta: phaseHeadline(phase),
        tone: 'live',
        data: truncateText(data?.arguments || '', 600)
      };
    case 'tool.after':
      return {
        icon: 'check-circle-2',
        title: `Tool finished: ${toolName || 'tool'}`,
        copy: summarizeToolOutputData(data?.display_output || 'Tool output recorded.'),
        meta: phaseHeadline(phase),
        tone: data?.is_error ? 'danger' : 'live',
        data: data?.metadata ? prettyJSON(data.metadata) : ''
      };
    case 'tool.blocked':
      return {
        icon: 'shield-alert',
        title: `Tool blocked: ${toolName || 'tool'}`,
        copy: data?.reason || 'The runtime guard blocked this tool call.',
        meta: phaseHeadline(phase),
        tone: 'danger',
        data: ''
      };
    case 'tool.interrupted':
      return {
        icon: 'hand',
        title: `Tool interrupted: ${toolName || 'tool'}`,
        copy: 'The tool was interrupted before it could complete.',
        meta: phaseHeadline(phase),
        tone: 'queued',
        data: ''
      };
    case 'session.child.queued':
      return {
        icon: 'git-branch-plus',
        title: 'Child job queued',
        copy: agentLabel(data?.agent_name, data?.agent_role) || 'Background child agent queued.',
        meta: data?.job_id ? shortId(data.job_id) : phaseHeadline(phase),
        tone: 'queued',
        data: ''
      };
    case 'session.child.spawned':
      return {
        icon: 'git-branch',
        title: 'Child session spawned',
        copy: agentLabel(data?.agent_name, data?.agent_role) || 'Child session created.',
        meta: data?.session_id ? shortId(data.session_id) : phaseHeadline(phase),
        tone: 'live',
        data: ''
      };
    case 'queue.job.claimed':
      return {
        icon: 'list-todo',
        title: 'Queue job claimed',
        copy: 'A worker picked up a queued background job.',
        meta: data?.job_id ? shortId(data.job_id) : phaseHeadline(phase),
        tone: 'queued',
        data: ''
      };
    case 'queue.job.completed':
    case 'queue.job.failed':
      return {
        icon: eventType === 'queue.job.failed' ? 'x-circle' : 'check-check',
        title: eventType === 'queue.job.failed' ? 'Background job failed' : 'Background job completed',
        copy: data?.agent_role ? `Role: ${data.agent_role}` : 'Background queue state changed.',
        meta: data?.job_id ? shortId(data.job_id) : phaseHeadline(phase),
        tone: eventType === 'queue.job.failed' ? 'danger' : 'live',
        data: ''
      };
    case 'queue.job.notified':
      return {
        icon: 'inbox',
        title: 'Background result notified',
        copy: 'Parent session received a background notification payload.',
        meta: data?.job_id ? shortId(data.job_id) : phaseHeadline(phase),
        tone: 'queued',
        data: ''
      };
    case 'goal.created':
    case 'goal.updated':
    case 'goal.paused':
    case 'goal.resumed':
    case 'goal.budget_limited':
    case 'goal.budget_wrapup_required':
    case 'goal.progress.recorded':
    case 'goal.completed':
    case 'goal.cleared':
      return {
        icon: eventType === 'goal.completed' ? 'badge-check' : eventType === 'goal.budget_limited' || eventType === 'goal.budget_wrapup_required' ? 'timer-off' : 'target',
        title: humanizeEventType(eventType),
        copy: data?.summary || data?.objective ? truncateText(data.summary || data.objective, 180) : 'Session goal state changed.',
        meta: data?.status ? humanizeStatus(data.status) : phaseHeadline(phase),
        tone: eventType === 'goal.budget_limited' || eventType === 'goal.budget_wrapup_required' ? 'queued' : eventType === 'goal.completed' ? 'live' : 'neutral',
        data: data ? prettyJSON(data) : ''
      };
    case 'mission.plan.updated':
    case 'mission.plan.approved':
    case 'mission.validation.updated':
      return {
        icon: eventType === 'mission.validation.updated' ? 'shield-check' : 'list-checks',
        title: goalPlanEventTitle(eventType),
        copy: data?.plan_status ? `Goal plan is ${humanizeStatus(data.plan_status)}.` : 'Goal plan state changed.',
        meta: data?.goal_id ? shortId(data.goal_id) : phaseHeadline(phase),
        tone: eventType === 'mission.plan.approved' ? 'live' : 'neutral',
        data: data ? prettyJSON(data) : ''
      };
    case 'planmode.created':
    case 'planmode.input_requested':
    case 'planmode.input_answered':
    case 'planmode.input_cancelled':
    case 'planmode.plan_submitted':
    case 'planmode.plan_approved':
    case 'planmode.plan_revised':
    case 'planmode.cancelled':
    case 'planmode.execution_started':
      return {
        icon: planModeEventIcon(eventType),
        title: planModeEventTitle(eventType),
        copy: planModeEventCopy(eventType, data),
        meta: data?.plan_version ? `v${data.plan_version}` : data?.status ? humanizeStatus(data.status) : phaseHeadline(phase),
        tone: eventType === 'planmode.cancelled' || eventType === 'planmode.input_cancelled'
          ? 'danger'
          : eventType === 'planmode.execution_started' || eventType === 'planmode.plan_approved'
            ? 'live'
            : 'queued',
        data: data ? prettyJSON(data) : ''
      };
    case 'session.steer.requested':
    case 'session.steer.queued':
    case 'session.steer.accepted':
    case 'session.steer.deferred':
    case 'session.steer.interrupt_requested':
      return {
        icon: 'corner-down-left',
        title: humanizeEventType(eventType),
        copy: 'Steer state changed for the current session.',
        meta: phaseHeadline(phase),
        tone: eventType.includes('deferred') ? 'queued' : 'live',
        data: data ? prettyJSON(data) : ''
      };
    case 'provider.cancelled':
      return {
        icon: 'ban',
        title: 'Provider request cancelled',
        copy: data?.reason || 'The in-flight provider call was cancelled.',
        meta: phaseHeadline(phase),
        tone: 'danger',
        data: ''
      };
    case 'session.paused':
      return {
        icon: 'pause-circle',
        title: data?.reason === 'manual_stop' ? 'Run stopped' : 'Run paused',
        copy: data?.reason === 'manual_stop'
          ? 'The active run was stopped and can be reviewed or continued later.'
          : 'The active run paused at a safe boundary.',
        meta: phaseHeadline(phase),
        tone: 'queued',
        data: ''
      };
    case 'session.completed':
      return {
        icon: 'check-check',
        title: 'Session completed',
        copy: 'The run finished cleanly.',
        meta: phaseHeadline(phase),
        tone: 'live',
        data: ''
      };
    case 'session.failed':
      return {
        icon: 'x-circle',
        title: 'Session failed',
        copy: data?.error || 'The run failed.',
        meta: phaseHeadline(phase),
        tone: 'danger',
        data: ''
      };
    default:
      return {
        icon: 'dot',
        title: humanizeEventType(eventType || 'event'),
        copy: 'Durable runtime event recorded.',
        meta: [phaseHeadline(phase), eventID ? shortId(eventID) : ''].filter(Boolean).join(' · '),
        tone: 'neutral',
        data: data ? truncateText(prettyJSON(data), 600) : ''
      };
  }
}

function summarizeToolArgumentsData(value) {
  const parsed = parseMaybeJSON(value);
  if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
    if (typeof parsed.path === 'string' && parsed.path) {
      return shortenPath(parsed.path);
    }
    if (typeof parsed.pattern === 'string' && parsed.pattern) {
      return `pattern: ${parsed.pattern}`;
    }
    if (typeof parsed.prompt === 'string' && parsed.prompt) {
      return truncateText(parsed.prompt, 100);
    }
  }
  return compactText(value, 100) || 'Tool call is running.';
}

function summarizeToolOutputData(value) {
  return compactText(value, 120) || 'Tool output recorded.';
}

function updateLiveActivityFromEvent(event) {
  if (!shouldPromoteLiveActivity(event.type)) {
    return;
  }
  const descriptor = describeEventDescriptor(event.type, event.data, event.phase, event.id);
  state.liveActivity = {
    title: descriptor.title,
    copy: descriptor.copy,
    tone: descriptor.tone || 'neutral'
  };
}

function shouldPromoteLiveActivity(type) {
  return [
    'provider.call',
    'tool.before',
    'tool.after',
    'tool.blocked',
    'tool.interrupted',
    'session.child.spawned',
    'session.child.queued',
    'queue.job.claimed',
    'queue.job.completed',
    'queue.job.failed',
    'planmode.input_requested',
    'planmode.input_answered',
    'planmode.input_cancelled',
    'planmode.plan_submitted',
    'planmode.plan_approved',
    'planmode.plan_revised',
    'planmode.cancelled',
    'planmode.execution_started',
    'provider.cancelled',
    'session.paused',
    'session.completed',
    'session.failed'
  ].includes(type || '');
}

function shouldRefreshAfterEvent(type) {
  return [
    'assistant.message',
    'tool.after',
    'tool.blocked',
    'tool.interrupted',
    'planmode.input_requested',
    'planmode.input_answered',
    'planmode.input_cancelled',
    'planmode.plan_submitted',
    'planmode.plan_approved',
    'planmode.plan_revised',
    'planmode.cancelled',
    'planmode.execution_started',
    'session.steer.accepted',
    'session.background.accepted',
    'session.child.spawned'
  ].includes(type);
}

function needsOverviewRefresh(type) {
  return typeof type === 'string' && (type.startsWith('session.child') || type.startsWith('queue.') || type.startsWith('planmode.'));
}

function humanizeEventType(value) {
  return humanizeStatus(String(value || '').replaceAll('.', ' '));
}

function goalPlanEventTitle(eventType) {
  switch (eventType) {
    case 'mission.plan.updated':
      return 'Goal plan updated';
    case 'mission.plan.approved':
      return 'Goal plan approved';
    case 'mission.validation.updated':
      return 'Goal validation updated';
    default:
      return humanizeEventType(eventType);
  }
}

function planModeEventIcon(eventType) {
  switch (eventType) {
    case 'planmode.plan_approved':
    case 'planmode.execution_started':
      return 'badge-check';
    case 'planmode.cancelled':
    case 'planmode.input_cancelled':
      return 'x-circle';
    case 'planmode.input_requested':
      return 'message-circle-question';
    case 'planmode.plan_submitted':
      return 'clipboard-check';
    default:
      return 'map';
  }
}

function planModeEventTitle(eventType) {
  switch (eventType) {
    case 'planmode.created':
      return 'Plan Mode started';
    case 'planmode.input_requested':
      return 'Plan input requested';
    case 'planmode.input_answered':
      return 'Plan input answered';
    case 'planmode.input_cancelled':
      return 'Plan input cancelled';
    case 'planmode.plan_submitted':
      return 'Plan submitted';
    case 'planmode.plan_approved':
      return 'Plan approved';
    case 'planmode.plan_revised':
      return 'Plan revision requested';
    case 'planmode.cancelled':
      return 'Plan Mode cancelled';
    case 'planmode.execution_started':
      return 'Approved plan running';
    default:
      return humanizeEventType(eventType);
  }
}

function planModeEventCopy(eventType, data) {
  if (eventType === 'planmode.plan_submitted') {
    return data?.summary || 'A plan is waiting for approval.';
  }
  if (eventType === 'planmode.input_requested') {
    const count = Number(data?.questions || data?.pending_questions || 1);
    return `${count} planning question${count === 1 ? '' : 's'} waiting for an answer.`;
  }
  if (eventType === 'planmode.execution_started') {
    return 'The approved plan was appended as a durable user action and execution resumed.';
  }
  return data?.objective || data?.status || 'Plan Mode state changed.';
}
