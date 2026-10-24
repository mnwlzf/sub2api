export default {
  codex: {
    timezone: {
      label: 'Request timezone',
      hint: 'Used for the OpenAI request Date header and time-based fingerprints. Leave as default to follow {timezone}.',
      placeholder: 'Select a request timezone',
      followDefault: 'Follow default',
      loadFailed: 'Failed to load request timezones. The account can still be saved with the default timezone.'
    },
    ticket: {
      title: 'Codex ticket center · {name}',
      loadFailed: 'Failed to load Codex ticket data',
      openCenterHint: 'Click to open the ticket center',
      expiresAt: 'Expires at {time}',
      reasonLabel: 'Reason: {reason}',
      moreModels: '+{count} more models',
      state: {
        ready: 'Ready',
        expiring: 'Expiring',
        expired: 'Expired',
        missing: 'Missing',
        blocked: 'Blocked',
        paused: 'Paused',
        disabled: 'Harvesting off',
        not_participating: 'Not participating',
        unavailable: 'Unavailable'
      },
      summary: {
        allReady: 'All models ready',
        blocked: 'Some models blocked',
        missing: 'Ticket missing/expired',
        disabled: 'Ticket harvesting disabled',
        notParticipating: 'Account not participating',
        partial: 'Partially ready',
        unavailable: 'Not applicable'
      },
      reason: {
        ready: 'Ready',
        rate_limited: 'Rate limited / backing off',
        participation_disabled: 'Model participation disabled',
        disabled: 'Ticket harvesting disabled globally',
        ticket_unavailable: 'Ticket unavailable'
      },
      global: {
        enabled: 'Global ticket harvesting is enabled',
        disabled: 'Global ticket harvesting is disabled — manual harvest is unavailable',
        allowWithoutTicketOn: 'Requests without a ticket are allowed',
        allowWithoutTicketOff: 'Requests without a ticket are blocked',
        poolAll: 'Proxy pool: all available proxies',
        poolCustom: 'Proxy pool: {count} selected proxies',
        availableProxies: '{count} proxies available for harvesting',
        cadence: 'Retry {min}-{max}s · refresh {refresh}s',
        loadFailed: 'Failed to load global ticket settings'
      },
      participation: {
        title: 'Account / model participation',
        description: 'Toggle whether this account (and each model) participates in ticket harvesting.',
        accountEnabled: 'Account participates'
      },
      tabs: {
        current: 'Current ticket',
        history: 'Harvest history',
        events: 'Event timeline',
        invalidations: 'Invalidation history'
      },
      current: {
        model: 'Model',
        harvest: 'Harvest now',
        harvesting: 'Harvesting...',
        harvestResult: 'Harvest attempt finished: {outcome} · {model} · {duration} ms',
        noTicket: 'No ticket status is available for this account',
        ticketState: 'Ticket state',
        length: 'Ticket length',
        lengthMismatch: '(unexpected)',
        capturedAt: 'Captured at',
        expiresAt: 'Expires at',
        remaining: 'Remaining',
        reason: 'Reason',
        quotaResetAt: 'Quota resets at',
        components: 'Components',
        turnState: 'Turn state present',
        cookie: 'Routing cookie present',
        harvestEnabled: 'Harvest enabled',
        harvestPaused: 'Harvest paused',
        blocked: 'Gated as blocked'
      },
      manualUnavailable: {
        disabled: 'Manual harvest is unavailable because global ticket harvesting is disabled',
        no_proxy: 'Manual harvest is unavailable because no ticket proxy is available',
        unavailable: 'Manual harvest is currently unavailable'
      },
      columns: {
        time: 'Time',
        model: 'Model',
        outcome: 'Outcome',
        trigger: 'Trigger',
        httpStatus: 'HTTP',
        ticketLength: 'Ticket length',
        duration: 'Duration',
        proxy: 'Proxy',
        reason: 'Reason',
        kind: 'Kind',
        detail: 'Detail',
        route: 'Route',
        invalidatedCurrent: 'Invalidated current',
        generation: 'Generation'
      },
      history: {
        total: '{total} attempts',
        filterAll: 'All attempts',
        filterSuccess: 'Success only'
      },
      events: {
        total: '{total} events',
        filterAll: 'All events',
        filterAttempts: 'Attempts',
        filterSuccess: 'Successful attempts',
        filterFailure: 'Failed attempts',
        filterInvalidation: 'Invalidations',
        invalidatedCurrent: 'current ticket invalidated'
      },
      invalidations: {
        total: '{total} invalidations',
        detailTitle: 'Invalidation detail',
        presence: 'Ticket presence',
        presenceValue: 'Original ticket: {originalTicket} · original cookie: {originalCookie} · returned cookie: {returnedCookie} · returned set-cookies: {setCookies}'
      }
    },
    ticketSettings: {
      title: 'Codex ticket harvesting',
      description: 'Global switch, retry cadence and proxy pool for the x-codex-turn-state ticket harvester.',
      loadFailed: 'Failed to load ticket settings',
      saveFailed: 'Failed to save ticket settings',
      enabled: {
        title: 'Enable ticket harvesting',
        description: 'When disabled, no tickets are harvested and gated models fall back to the allow-without-ticket policy.'
      },
      allowWithoutTicket: {
        title: 'Allow requests without a ticket',
        description: 'When enabled, requests for gated models are still forwarded even if no ready ticket exists.'
      },
      cadence: {
        title: 'Retry and refresh cadence',
        description: 'Failure backoff range and active refresh interval for ready tickets.',
        retryMin: 'Retry min (seconds)',
        retryMax: 'Retry max (seconds)',
        refresh: 'Refresh interval (seconds)',
        errors: {
          retryMinPositive: 'Retry min must be at least 1 second',
          retryMaxPositive: 'Retry max must be at least 1 second',
          refreshPositive: 'Refresh interval must be at least 1 second',
          retryMinGreaterThanMax: 'Retry min must not be greater than retry max'
        }
      },
      pool: {
        title: 'Harvesting proxy pool',
        description: 'Reuse the proxies configured under IP settings. Custom mode rotates only the selected proxies.',
        modeAll: 'All available proxies',
        modeCustom: 'Custom proxy list',
        available: '{count} proxies currently available',
        empty: 'No proxies found. Create proxies under IP settings first.',
        errors: {
          noProxySelected: 'Select at least one proxy for custom mode'
        }
      },
      configOnly: {
        title: 'Config-file-only parameters',
        description: 'The following keys are read from the backend config file and are not writable through the admin API:'
      }
    },
    degradation: {
      title: 'Codex degradation check · {name}',
      runFailed: 'Degradation check failed',
      status: {
        normal: 'Normal',
        degraded: 'Degraded',
        uncertain: 'Uncertain',
        failed: 'Failed'
      },
      models: {
        title: 'Models to probe',
        description: 'Each selected model runs one native ModelTrace probe with the account credentials.',
        bank: 'Fingerprint bank: {bank}',
        selected: '{count} selected',
        empty: 'No diagnostic models are available',
        required: 'Select at least one model'
      },
      run: 'Run check',
      running: 'Checking...',
      runningHint: 'Probing {count} model(s) with the account credentials. Elapsed: {elapsed}s (each model may take up to 120s).',
      result: {
        title: 'Check result',
        checkedAt: 'Checked at {time}',
        reason: 'Overall reason: {reason}',
        pending: 'Pending',
        predictedModel: 'Predicted model',
        probability: 'Probability',
        parsedCount: 'Parsed numbers',
        httpStatus: 'HTTP',
        reasonLabel: 'Reason'
      },
      history: {
        title: 'Recent probe results',
        total: '{total} records',
        refresh: 'Refresh history',
        time: 'Time',
        model: 'Model',
        status: 'Status',
        predictedModel: 'Predicted model',
        probability: 'Probability',
        source: 'Source'
      }
    },
    degradationSettings: {
      title: 'Automatic degradation detection',
      description: 'Periodically probe OpenAI accounts with ModelTrace fingerprints and temporarily block degraded accounts.',
      loadFailed: 'Failed to load degradation settings',
      saveFailed: 'Failed to save degradation settings',
      interval: 'Check interval (minutes)',
      block: 'Block duration after degradation (minutes)',
      enabled: {
        title: 'Enable automatic detection',
        description: 'When disabled, the background runner produces no upstream probes.'
      },
      models: {
        title: 'Probe models',
        description: 'Only models from the embedded fingerprint bank can be selected.',
        bank: 'Fingerprint bank: {bank}',
        empty: 'No diagnostic models are available'
      },
      errors: {
        intervalPositive: 'Check interval must be at least 1 minute',
        blockPositive: 'Block duration must be at least 1 minute',
        modelsRequired: 'Select at least one probe model',
        modelsNotAvailable: 'The selected models are not in the current fingerprint bank'
      },
      runNow: {
        action: 'Run one cycle now',
        running: 'Triggering...',
        hint: 'Triggers one automatic detection cycle immediately. No probes are produced while the feature is disabled.',
        triggered: 'Detection cycle triggered',
        failed: 'Failed to trigger detection cycle'
      },
      template: {
        title: 'Probe template (JSONL)',
        description: 'The template is validated before saving; invalid JSONL is rejected. Empty content falls back to the embedded default.',
        placeholder: 'Paste the JSONL probe template here',
        restoreDefault: 'Restore default',
        hint: 'Restoring the default clears the stored override and uses the embedded template.',
        required: 'Template content is required',
        invalid: 'The template is invalid and was not saved',
        saved: 'Probe template saved',
        restored: 'Default probe template restored'
      },
      configOnly: {
        title: 'Config-file-only parameters',
        description: 'The following keys are read from the backend config file and are not writable through the admin API:'
      }
    }
  }
}
