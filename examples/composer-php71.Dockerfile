# Composer for PHP 7.1 modules, for use with [composer] docker_image.
#   docker build -t presto-composer:php7.1 -f examples/composer-php71.Dockerfile examples
# Composer 2.2 is the last LTS release that runs on PHP 7.1. Add the extensions your
# locked packages require (ext-* in composer.lock) to the apt-get line.
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git gnupg unzip \
    && curl -fsSL https://packages.sury.org/php/apt.gpg | gpg --dearmor -o /usr/share/keyrings/sury-php.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://packages.sury.org/php/ bookworm main" > /etc/apt/sources.list.d/sury-php.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
       php7.1-cli php7.1-curl php7.1-bcmath php7.1-gd php7.1-intl php7.1-mbstring \
       php7.1-mysql php7.1-soap php7.1-xml php7.1-zip \
    && curl -fsSL https://getcomposer.org/installer | php -- --2.2 --install-dir=/usr/local/bin --filename=composer \
    && apt-get purge -y gnupg && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*

CMD ["composer", "--version"]
