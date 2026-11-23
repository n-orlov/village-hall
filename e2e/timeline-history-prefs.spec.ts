import { test } from './fixtures';
import { selectRoute, viewports, walk } from './history-prefs-walk';

// The Timeline's entries each restore their own filters on Back and Forward (PRD R7): see history-prefs-walk.ts. Named
// timeline*.spec.ts so that it runs early, before the store has grown.
for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the Timeline: Back to the default entry shows the default window filters, Back to the filtered entry its filter', async ({ page }) => {
      await walk(page, selectRoute('#/timeline', 'closed-filter', '30', 'closed=30'));
    });
  });
}
