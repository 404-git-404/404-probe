'use strict';

// Keep test output separate from case records. A case name comes from the
// runner's events, including when a test prints text resembling TAP.
module.exports = async function* (events) {
  for await (const {type, data} of events) {
    if (type === 'test:pass' || type === 'test:fail') {
      const error = data.details?.error;
      if (error?.failureType === 'subtestsFailed') continue;
      yield JSON.stringify({
        type, name: data.name, skip: data.skip || data.todo, failureType: error?.failureType,
        error: error?.cause?.stack || error?.stack,
      }) + '\n';
    } else if (type === 'test:stdout' || type === 'test:stderr') {
      yield JSON.stringify({type, message: data.message}) + '\n';
    }
  }
};
