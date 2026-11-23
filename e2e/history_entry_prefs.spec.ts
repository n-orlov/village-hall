import { test } from './fixtures';
import { apiContext, post, selectRoute, sortRoute, viewports, walk } from './history-prefs-walk';

// Each history entry restores its own filters and sort on Back and Forward (PRD R7); the walk is in history-prefs-walk.ts.

for (const vp of viewports) {
  const phone = vp.name === 'phone';
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the Tasks view: Back to the default entry shows the defaults, Back to the filtered entry its filter', async ({ page }) => {
      await walk(page, selectRoute('#/tasks', 'status-filter', 'all', 'status=all'));
    });

    test('the Initiatives list: Back to the default entry shows the defaults, Back to the filtered entry its filter', async ({ page }) => {
      await walk(page, selectRoute('#/initiatives', 'initiative-filter', 'all', 'status=all'));
    });

    test('the Archive view: Back to the default entry shows the default window, Back to the other entry its window', async ({ page }) => {
      await walk(page, selectRoute('#/archive', 'archive-window', '90', 'window=90'));
    });

    test('the Actors view: Back to the default entry shows the default order, Back to the sorted entry its sort', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-hep-act-${vp.name}-${Date.now().toString(36)}`;
      for (const name of ['mike', 'bob']) {
        const api = await apiContext(playwright, baseURL, `zzhep-${vp.name}-${name}`);
        if (name === 'mike') await post(api, '/api/v1/initiatives', { slug, title: `History entry prefs ${vp.name}` }, 201);
        const task = (await post(api, '/api/v1/tasks', { initiative: slug, title: `Held by ${name}` }, 201)).ref;
        await post(api, '/api/v1/leases', { task, intent: `work of ${name}`, resources: [`repo:${slug}-${name}`] }, 201);
        await api.dispose();
      }
      await walk(page, sortRoute('#/actors', 'actor', true) /* the shared sort control is a select at every width */);
    });

    test('Settings -> API keys: Back to the default entry shows the default order, Back to the sorted entry its sort', async ({ page }) => {
      await walk(page, sortRoute('#/settings/keys', 'name', phone));
    });

    test('the initiative page: Back to the default entry shows the default task filter, Back to the filtered entry its filter', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-hep-page-${vp.name}-${Date.now().toString(36)}`;
      await post(api, '/api/v1/initiatives', { slug, title: `History entry prefs page ${vp.name}` }, 201);
      await post(api, '/api/v1/tasks', { initiative: slug, title: `Task of ${slug}` }, 201);
      await walk(page, selectRoute(`#/i/${slug}`, 'task-filter', 'all', 'status=all'));
    });
  });
}
