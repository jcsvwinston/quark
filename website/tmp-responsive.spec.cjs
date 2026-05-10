const {test} = require('@playwright/test');

const paths = [
  '/',
  '/docs/guides/getting-started',
  '/docs/guides/querying',
  '/docs/reference/api/query-builder',
];

for (const path of paths) {
  test(path, async ({page}) => {
    await page.setViewportSize({width: 390, height: 844});
    await page.goto('http://127.0.0.1:3000/quark-docs' + path, {waitUntil: 'networkidle'});

    const info = await page.evaluate(() => {
      const vw = window.innerWidth;
      const scrollWidth = Math.max(document.documentElement.scrollWidth, document.body.scrollWidth);
      const offenders = [...document.querySelectorAll('body *')]
        .map((el) => {
          const r = el.getBoundingClientRect();
          return {
            tag: el.tagName,
            cls: String(el.className || '').slice(0, 100),
            text: (el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 90),
            left: Math.round(r.left),
            right: Math.round(r.right),
            width: Math.round(r.width),
          };
        })
        .filter((x) => x.right > vw + 2 || x.left < -2)
        .slice(0, 30);

      return {vw, scrollWidth, offenders};
    });

    console.log(path + ' ' + JSON.stringify(info, null, 2));
  });
}
