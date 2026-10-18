// Keeps the page the person asked for through the sign-in: the hash is
// not sent to the server, so the forms carry it from here.
(function () {
  var next = location.pathname + location.search + location.hash;
  if (next === '/signin' || next.indexOf('/signin?') === 0 || next.indexOf('/signin/') === 0) {
    var m = /[?&]next=([^&]*)/.exec(location.search);
    next = m ? decodeURIComponent(m[1]) : '/';
  }
  var fields = document.querySelectorAll('input[data-next]');
  for (var i = 0; i < fields.length; i++) fields[i].value = next;
  var links = document.querySelectorAll('a[data-next-link]');
  for (var j = 0; j < links.length; j++) links[j].href = '/signin/start?next=' + encodeURIComponent(next);
})();

// The first-run page says at once when the two passwords differ, rather
// than after a round trip that empties them.
(function () {
  var pw = document.querySelector('input[name=password]');
  var again = document.querySelector('input[name=again]');
  if (!pw || !again) return;
  var check = function () {
    again.setCustomValidity(again.value && again.value !== pw.value ? 'The two passwords are not the same.' : '');
  };
  pw.addEventListener('input', check);
  again.addEventListener('input', check);
})();
